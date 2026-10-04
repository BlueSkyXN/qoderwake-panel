import copy
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'panel'))
from deletion_preflight import DeletionPreflight


class Sources:
    def __init__(self):
        self.data = {
            'wakers': [
                {'agentId': 'w1', 'name': 'One'},
                {'agentId': 'w2', 'name': 'Two'}
            ],
            'triggers': [],
            'channels': [],
            'pairings': [],
            'groups': [],
            'workflows': {None: [], 'w1': [], 'w2': []},
            'sessions': {'w1': [], 'w2': []},
            'preferences': {
                'w1': {'noProject': 'other/model', 'byProject': {}},
                'w2': {'noProject': '', 'byProject': {}}
            },
            'callers': [],
            'details': {
                'w1': {'agentId': 'w1', 'skills': []},
                'w2': {'agentId': 'w2', 'skills': []}
            },
            'activity': {
                'runningConversationTasks': 0,
                'runningTriggerTasks': 0,
                'queuedTriggerTasks': 0
            },
            'models': {'provider': ['provider/model']}
        }
        self.fail = set()

    def _get(self, name, value):
        if name in self.fail:
            raise RuntimeError('unavailable')
        return copy.deepcopy(value)

    def wakers(self): return self._get('wakers', self.data['wakers'])
    def triggers(self): return self._get('triggers', self.data['triggers'])
    def channels(self): return self._get('channels', self.data['channels'])
    def pairings(self): return self._get('pairings', self.data['pairings'])
    def groups(self): return self._get('groups', self.data['groups'])
    def callers(self): return self._get('callers', self.data['callers'])
    def activity(self): return self._get('activity', self.data['activity'])
    def waker_detail(self, waker): return self._get('detail', self.data['details'][waker])
    def workflows(self, waker=None): return self._get('workflows', self.data['workflows'][waker])
    def sessions(self, waker): return self._get('sessions', self.data['sessions'][waker])
    def preference(self, waker): return self._get('preference', self.data['preferences'][waker])
    def provider_models(self, name): return self._get('provider_models', self.data['models'][name])


class DeletionPreflightTests(unittest.TestCase):
    def setUp(self):
        self.sources = Sources()
        self.preflight = DeletionPreflight(self.sources)

    @staticmethod
    def checks(result):
        return {row['id']: row for row in result['checks']}

    def test_waker_clear_required_checks_with_explicit_warnings(self):
        result = self.preflight.scan('waker', 'w1')
        checks = self.checks(result)
        self.assertTrue(result['deletable'])
        self.assertEqual(result['completeness'], 'partial')
        self.assertEqual(checks['system_identity']['status'], 'clear')
        self.assertEqual(checks['active_session']['status'], 'clear')
        self.assertEqual(checks['installed_content_boundary']['status'], 'warning')
        self.assertIn('installed_content_boundary', result['warningsRequired'])

    def test_waker_all_known_blockers_are_reported(self):
        self.sources.data['callers'] = [{'id': 'caller1', 'name': 'Caller', 'wakers': ['w1'], 'revoked': False, 'expires': 4102444800}]
        self.sources.data['triggers'] = [{'triggerId': 't1', 'target': {'workerId': 'w1'}}]
        self.sources.data['channels'] = [{'channelId': 'c1', 'config': {'bindingTarget': {'targetId': 'w1'}}}]
        self.sources.data['pairings'] = [{'pendingId': 'p1', 'targets': [{'targetId': 'w1'}]}]
        self.sources.data['groups'] = [{
            'group': {'groupId': 'g1', 'name': 'Group'},
            'members': [{'wakerKey': 'w1', 'model': 'other/model'}],
            'leader': {'wakerKey': 'w2'}
        }]
        self.sources.data['workflows']['w1'] = [{'workflowId': 'wf1', 'title': 'Flow'}]
        self.sources.data['sessions']['w1'] = [
            {'session_id': 's-active', 'session_status': 'needs_restart'},
            {'session_id': 's-old', 'session_status': 'success'}
        ]
        result = self.preflight.scan('waker', 'w1')
        checks = self.checks(result)
        self.assertFalse(result['deletable'])
        for name in ('caller_scope', 'trigger_target', 'channel_target',
                     'group_membership', 'scoped_workflow', 'active_session'):
            self.assertEqual(checks[name]['status'], 'blocked', name)
        self.assertEqual(checks['pairing_target']['status'], 'warning')
        self.assertFalse(checks['pairing_target']['required'])
        self.assertEqual(checks['session_history']['status'], 'warning')

    def test_required_source_failure_or_unattributed_tasks_fail_closed(self):
        self.sources.fail.add('groups')
        result = self.preflight.scan('waker', 'w1')
        self.assertFalse(result['deletable'])
        self.assertEqual(self.checks(result)['group_membership']['status'], 'unknown')
        self.sources.fail.clear()
        self.sources.data['activity']['queuedTriggerTasks'] = 1
        result = self.preflight.scan('waker', 'w1')
        self.assertFalse(result['deletable'])
        self.assertEqual(self.checks(result)['task_attribution']['status'], 'unknown')

    def test_provider_matches_exact_models_not_prefixes(self):
        self.sources.data['preferences']['w1']['noProject'] = 'provider/model-extra'
        self.sources.data['triggers'] = [{'triggerId': 'safe', 'model': 'provider/model-extra'}]
        self.sources.data['channels'] = [{'channelId': 'safe', 'config': {'model': 'provider/model2'}}]
        self.sources.data['groups'] = [{
            'group': {'groupId': 'g1'},
            'members': [{'wakerKey': 'w1', 'model': 'provider/model-extra'}],
            'leader': {'wakerKey': 'w1'}
        }]
        result = self.preflight.scan('provider', 'provider', 'a' * 64)
        checks = self.checks(result)
        self.assertTrue(result['deletable'])
        for name in ('model_preference', 'trigger_model', 'channel_model', 'group_model'):
            self.assertEqual(checks[name]['status'], 'clear', name)

        self.sources.data['triggers'].append({
            'triggerId': 'blocked',
            'executionTarget': {'model': {'modelId': 'provider/model'}}
        })
        result = self.preflight.scan('provider', 'provider', 'a' * 64)
        self.assertFalse(result['deletable'])
        self.assertEqual(self.checks(result)['trigger_model']['refs'], ['blocked'])

    def test_provider_active_session_missing_model_is_unknown(self):
        self.sources.data['sessions']['w1'] = [{
            'session_id': 'active', 'session_status': 'running'
        }]
        result = self.preflight.scan('provider', 'provider', 'a' * 64)
        check = self.checks(result)['active_session_model']
        self.assertEqual(check['status'], 'unknown')
        self.assertFalse(result['deletable'])

    def test_channel_string_target_blocks_and_malformed_target_is_unknown(self):
        self.sources.data['channels'] = [{'channelId': 'c1', 'config': {'bindingTarget': 'w1'}}]
        result = self.preflight.scan('waker', 'w1')
        self.assertEqual(self.checks(result)['channel_target']['status'], 'blocked')
        self.sources.data['channels'] = [{'channelId': 'c2', 'config': {'bindingTarget': {'future': 'w1'}}}]
        result = self.preflight.scan('waker', 'w1')
        self.assertEqual(self.checks(result)['channel_target']['status'], 'unknown')
        self.assertFalse(result['deletable'])

    def test_unknown_or_missing_session_state_never_counts_as_history(self):
        for row in ({'session_id': 'missing'},
                    {'session_id': 'future', 'session_status': 'future_running_state'}):
            self.sources.data['sessions']['w1'] = [row]
            waker = self.preflight.scan('waker', 'w1')
            provider = self.preflight.scan('provider', 'provider', 'a' * 64)
            self.assertEqual(self.checks(waker)['active_session']['status'], 'unknown')
            self.assertEqual(self.checks(provider)['active_session_model']['status'], 'unknown')
            self.assertFalse(waker['deletable'])
            self.assertFalse(provider['deletable'])

    def test_group_leader_model_and_workflow_boundaries(self):
        self.sources.data['groups'] = [{
            'group': {'groupId': 'g1'}, 'members': [],
            'leader': {'wakerKey': 'w1', 'model': 'provider/model'}
        }]
        result = self.preflight.scan('provider', 'provider', 'a' * 64)
        self.assertEqual(self.checks(result)['group_model']['status'], 'blocked')
        self.sources.data['groups'] = []
        self.sources.data['workflows'][None] = [
            {'workflowId': 'safe', 'script': 'use provider/model-extra'},
            {'workflowId': 'match', 'script': 'model = "provider/model"'}
        ]
        result = self.preflight.scan('provider', 'provider', 'a' * 64)
        self.assertEqual(self.checks(result)['workflow_text_reference']['refs'], ['match'])

    def test_waker_fingerprint_changes_when_resource_changes(self):
        first = self.preflight.scan('waker', 'w1')
        self.sources.data['details']['w1']['description'] = 'changed'
        second = self.preflight.scan('waker', 'w1')
        self.assertNotEqual(first['revision'], second['revision'])
        self.assertNotEqual(first['impactDigest'], second['impactDigest'])

    def test_malformed_model_preferences_are_unknown(self):
        for value in ({}, {'unexpected': 'provider/model'}, {'noProject': ['provider/model']},
                      {'byProject': {'project': 123}},
                      {'noProject': {'future': 'provider/model'}}):
            self.sources.data['preferences']['w1'] = value
            result = self.preflight.scan('provider', 'provider', 'a' * 64)
            self.assertEqual(self.checks(result)['model_preference']['status'], 'unknown')
            self.assertFalse(result['deletable'])

    def test_malformed_channel_envelopes_are_unknown(self):
        for row in ({'channel': None}, {'config': []}, {'channel': {'config': False}}):
            self.sources.data['channels'] = [row]
            for kind, target in (('waker', 'w1'), ('provider', 'provider')):
                result = self.preflight.scan(kind, target)
                key = 'channel_target' if kind == 'waker' else 'channel_model'
                self.assertEqual(self.checks(result)[key]['status'], 'unknown')
                self.assertFalse(result['deletable'])

    def test_malformed_or_missing_trigger_target_is_unknown(self):
        for value in ({'workerId': ['w1']}, {}, {'future': 'w1'}):
            self.sources.data['triggers'] = [{'triggerId': 'fixture', 'target': value}]
            result = self.preflight.scan('waker', 'w1')
            self.assertEqual(self.checks(result)['trigger_target']['status'], 'unknown')
            self.assertFalse(result['deletable'])

    def test_missing_waker_identity_and_group_member_fail_closed(self):
        self.sources.data['details']['w1'] = {}
        result = self.preflight.scan('waker', 'w1')
        self.assertEqual(self.checks(result)['system_identity']['status'], 'unknown')
        self.assertFalse(result['deletable'])
        self.sources.data['details']['w1'] = {'agentId': 'w1'}
        self.sources.data['groups'] = [{'members': [{}], 'leader': 'w2'}]
        result = self.preflight.scan('waker', 'w1')
        self.assertEqual(self.checks(result)['group_membership']['status'], 'unknown')
        self.assertFalse(result['deletable'])

    def test_digest_is_stable_but_changes_with_impact(self):
        first = self.preflight.scan('waker', 'w1')
        self.sources.data['wakers'].reverse()
        second = self.preflight.scan('waker', 'w1')
        self.assertEqual(first['impactDigest'], second['impactDigest'])
        self.sources.data['sessions']['w1'] = [{
            'session_id': 'old', 'session_status': 'success'
        }]
        third = self.preflight.scan('waker', 'w1')
        self.assertNotEqual(first['impactDigest'], third['impactDigest'])


if __name__ == '__main__':
    unittest.main()
