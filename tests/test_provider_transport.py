import ipaddress
from pathlib import Path
import socket
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0,str(Path(__file__).resolve().parents[1]/'panel'))
import provider_transport as transport


class ProviderTransportTests(unittest.TestCase):
    def rows(self,*ips):
        return [(socket.AF_INET6 if ':' in ip else socket.AF_INET,socket.SOCK_STREAM,6,'',(ip,443)) for ip in ips]

    def test_reject_private_mixed_dns_and_mapped_ipv6(self):
        for ips in [('10.0.0.1',),('169.254.169.254',),('127.0.0.1',),('::ffff:127.0.0.1',),('8.8.8.8','192.168.1.1')]:
            with patch.object(socket,'getaddrinfo',return_value=self.rows(*ips)), self.assertRaises(ValueError):
                transport.addresses('api.example.com',443)

    def test_only_explicit_loopback_allows_local_probe(self):
        with patch.object(socket,'getaddrinfo',return_value=self.rows('127.0.0.1')):
            self.assertEqual(len(transport.addresses('localhost',8080)),1)
        with patch.object(socket,'getaddrinfo',return_value=self.rows('8.8.8.8')), self.assertRaises(ValueError):
            transport.addresses('localhost',8080)

    def test_connection_uses_resolved_address_without_second_lookup(self):
        rows=self.rows('8.8.8.8')
        with patch.object(socket,'getaddrinfo') as dns, patch.object(socket,'socket') as factory:
            result=transport.connect_pinned('api.example.com',443,12,rows)
            dns.assert_not_called()
            result.connect.assert_called_once_with(('8.8.8.8',443))

    def test_probe_rejects_empty_or_header_injection_key_before_dns(self):
        for key in ('','value\r\nX-Test: injected'):
            with patch.object(socket,'getaddrinfo') as dns, self.assertRaises(ValueError):
                transport.probe('https://api.example.com/v1',key)
            dns.assert_not_called()


if __name__=='__main__':
    unittest.main()
