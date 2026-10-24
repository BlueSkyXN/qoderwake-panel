package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"qoderwake-panel/internal/apierror"
	"qoderwake-panel/internal/auth"
)

var secretValueRE = regexp.MustCompile(`(?i)((?:password|passwd|secret|token|api[_-]?key|authorization|cookie)["\s]*[:=]\s*)[^\r\n,}]+`)
var bearerRE = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/-]+=*`)
var apiKeyRE = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`)
var ansiRE = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]|\x1b\\][^\x07]*(?:\x07|\x1b\\\\)")

func Redact(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[已隐藏]")
		}
	}
	value = secretValueRE.ReplaceAllString(value, "${1}[已隐藏]")
	value = bearerRE.ReplaceAllString(value, "Bearer [已隐藏]")
	return apiKeyRE.ReplaceAllString(value, "[已隐藏]")
}
func partialOutput(value string, secrets []string) (string, string) {
	retained := 0
	for _, secret := range secrets {
		for size := min(len(value), len(secret)-1); size > retained; size-- {
			if strings.HasSuffix(value, secret[:size]) {
				retained = size
				break
			}
		}
	}
	if retained > 0 {
		return value[:len(value)-retained], value[len(value)-retained:]
	}
	return value, ""
}
func (j *Jobs) append(job *job, value string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	clean := Redact(value, job.secrets)
	clean = ansiRE.ReplaceAllString(clean, "")
	clean = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r >= 32 {
			return r
		}
		return -1
	}, clean)
	job.output = append(job.output, []rune(clean)...)
	if bytes := []byte(string(job.output)); len(bytes) > MaxOutput {
		tail := bytes[len(bytes)-MaxOutput:]
		for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
			tail = tail[1:]
		}
		next := []rune(string(tail))
		job.offset += int64(len(job.output) - len(next))
		job.output = next
	}
}
func (j *Jobs) knownSecrets(req Request, spec Spec) []string {
	out := []string{}
	add := func(value any) {
		switch v := value.(type) {
		case string:
			if v != "" {
				out = append(out, v)
			}
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok && s != "" {
					out = append(out, s)
				}
			}
		}
	}
	for k, v := range req.Context {
		if Secret.MatchString(k) {
			add(v)
		}
	}
	add(req.Stdin)
	for k, v := range req.Parameters.Options {
		if Secret.MatchString(k) || slices.Contains([]string{"--json", "--env", "--headers", "--header"}, k) {
			add(v)
		}
	}
	if slices.Contains([]string{"config.set", "settings.set", "settings.global.set"}, spec.ID) {
		for _, v := range req.Parameters.Arguments {
			add(v)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return len(b) - len(a) })
	return out
}
func (j *Jobs) run(parent context.Context, job *job, identity auth.Identity, spec Spec, req Request) {
	defer j.wg.Done()
	defer job.cancel()
	status, code := "failed", ""
	var exit *int
	var cmd *exec.Cmd
	var output *os.File
	dispatched := false
	defer func() {
		if output != nil {
			output.Close()
		}
		j.mu.Lock()
		job.terminal = nil
		job.secrets = nil
		j.mu.Unlock()
		j.setState(job, status, code, exit)
		if j.Audit != nil {
			j.Audit(identity, job.Result)
		}
	}()
	fail := func(err error) {
		var e *apierror.Error
		if errors.As(err, &e) {
			code = e.Code
		} else {
			code = "cli_execution_failed"
		}
		if dispatched && spec.Effect == "write" {
			status = "unknown"
		}
	}
	if j.Guard != nil {
		release, err := j.Guard(parent, identity, spec, req)
		if err != nil {
			fail(err)
			return
		}
		if release != nil {
			defer release()
		}
	}
	if parent.Err() != nil {
		status = "cancelled"
		return
	}
	detail, err := j.Detail(parent, identity, spec.ID)
	if err != nil {
		fail(err)
		return
	}
	if !detail.Available {
		code = detail.Error
		return
	}
	params := req.Parameters
	if j.Resolve != nil {
		params, err = j.Resolve(identity, params)
		if err != nil {
			fail(err)
			return
		}
	}
	argv, err := BuildArgv(spec, params)
	if err != nil {
		fail(err)
		return
	}
	cmd = exec.Command(j.Binary, argv...)
	cmd.Dir = j.Home
	cmd.Env = j.Environment()
	for k, v := range req.Context {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var child *os.File
	if req.Interactive {
		output, child, err = pty.Open()
		if err != nil {
			fail(err)
			return
		}
		if err = disableEcho(child); err != nil {
			child.Close()
			fail(err)
			return
		}
		cmd.Stdin = child
		cmd.Stdout = child
		cmd.Stderr = child
	} else {
		output, child, err = os.Pipe()
		if err != nil {
			fail(err)
			return
		}
		cmd.Stdout = child
		cmd.Stderr = child
		if req.Stdin != "" {
			cmd.Stdin = strings.NewReader(req.Stdin)
		}
	}
	if err = cmd.Start(); err != nil {
		child.Close()
		fail(err)
		return
	}
	child.Close()
	dispatched = true
	j.mu.Lock()
	job.secrets = j.knownSecrets(req, spec)
	if req.Interactive {
		job.terminal = output
	}
	j.mu.Unlock()
	j.setState(job, "running", "", nil)
	wait := make(chan error, 1)
	go func(done chan error) { done <- cmd.Wait(); close(done) }(wait)
	chunks := make(chan []byte, 8)
	go func(channel chan []byte) {
		defer close(channel)
		buf := make([]byte, 32768)
		for {
			n, e := output.Read(buf)
			if n > 0 {
				channel <- append([]byte{}, buf[:n]...)
			}
			if e != nil {
				return
			}
		}
	}(chunks)
	ctx, cancel := context.WithTimeout(parent, req.Timeout)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	cancelled := ctx.Done()
	var killTimer *time.Timer
	var killC <-chan time.Time
	defer func() {
		if killTimer != nil {
			killTimer.Stop()
		}
	}()
	finished := false
	var waitErr error
	pending := ""
	utf8Tail := []byte{}
	discardLine := false
	flush := func(final bool) {
		if pending == "" {
			return
		}
		if final {
			j.append(job, pending)
			pending = ""
			return
		}
		if req.Interactive {
			j.mu.Lock()
			secrets := append([]string{}, job.secrets...)
			j.mu.Unlock()
			ready, retained := partialOutput(pending, secrets)
			j.append(job, ready)
			pending = retained
		}
	}
	for wait != nil || chunks != nil {
		select {
		case data, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			data = append(utf8Tail, data...)
			utf8Tail = nil
			var decoded strings.Builder
			for len(data) > 0 {
				if !utf8.FullRune(data) {
					utf8Tail = append([]byte{}, data...)
					break
				}
				r, n := utf8.DecodeRune(data)
				decoded.WriteRune(r)
				data = data[n:]
			}
			pending += decoded.String()
			if discardLine {
				if at := strings.IndexByte(pending, '\n'); at >= 0 {
					pending = pending[at+1:]
					discardLine = false
				} else {
					pending = ""
				}
			}
			if at := strings.LastIndexByte(pending, '\n'); at >= 0 {
				j.append(job, pending[:at+1])
				pending = pending[at+1:]
			}
			if len(pending) > 131072 {
				j.append(job, "[单行输出超过上限，已省略]\n")
				pending = ""
				discardLine = true
			}
		case e := <-wait:
			wait = nil
			finished = true
			waitErr = e
			if code != "" {
				output.Close()
			}
		case <-cancelled:
			cancelled = nil
			code = "cli_timeout"
			if parent.Err() != nil {
				code = "cli_cancelled"
			}
			if !finished {
				syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
				killTimer = time.NewTimer(3 * time.Second)
				killC = killTimer.C
			} else {
				output.Close()
			}
		case <-killC:
			killC = nil
			if !finished {
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			output.Close()
		case <-tick.C:
			flush(false)
		}
	}
	if len(utf8Tail) > 0 {
		pending += strings.ToValidUTF8(string(utf8Tail), "�")
	}
	flush(true)
	value := cmd.ProcessState.ExitCode()
	exit = &value
	if code != "" {
		status = "cancelled"
		if spec.Effect == "write" {
			status = "unknown"
		}
	} else if waitErr == nil && value == 0 {
		status = "succeeded"
	} else {
		status = "failed"
	}
}
