import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec=importlib.util.spec_from_file_location('builder',Path(__file__).resolve().parents[1]/'ops/gateway-policy.py')
b=importlib.util.module_from_spec(spec);spec.loader.exec_module(b)


class PolicyBuilderTests(unittest.TestCase):
    def test_candidates_are_redacted_grouped_and_manual(self):
        rows=[(1,{'method':'GET','path':'/catalog','q':0,'status':200,'action':'forward','forward_attempted':True}),(2,{'method':'GET','path':'/catalog','q':0,'status':304,'action':'forward','forward_attempted':True}),(3,{'method':'POST','path':'/events','q':40,'status':200,'action':'forward','forward_attempted':True})]
        items=b.candidates(rows)
        self.assertEqual(items[0]['observed'],2)
        self.assertEqual(items[1]['max_body'],40)
        with self.assertRaises(ValueError):b.make_policy(items,[])
        policy=b.make_policy(items,[{'method':'GET','path':'/catalog','allow':True,'headers':['accept'],'query':['page'],'max_body':0}])
        self.assertEqual(policy['mode'],'strict')
        self.assertEqual(len(policy['rules']),1)
        self.assertNotIn('statuses',policy['rules'][0])

    def test_body_limit_and_headers_cannot_be_loosened_silently(self):
        items=[{'method':'POST','path':'/events','max_body':40}]
        for review in [
            {'method':'POST','path':'/events','allow':True,'max_body':39,'headers':[],'query':[]},
            {'method':'POST','path':'/events','allow':True,'max_body':40,'headers':['cookie'],'query':[]},
            {'method':'POST','path':'/events','allow':True,'max_body':40,'headers':[],'query':['bad.key']},
        ]:
            with self.assertRaises(ValueError):b.make_policy(items,[review])

    def test_reader_ignores_dynamic_or_unredacted_paths(self):
        with tempfile.TemporaryDirectory() as tmp:
            path=Path(tmp)/'log';path.write_text('\n'.join(map(json.dumps,[
                {'method':'GET','path':'/<redacted>','q':0,'action':'forward','forward_attempted':True},
                {'method':'GET','path':'/safe/path','q':0,'action':'forward','forward_attempted':True},
                {'method':'CONNECT','path':'/safe/path','q':0,'action':'forward','forward_attempted':True},
                {'method':'GET','path':'/user@example.com','q':0,'action':'forward','forward_attempted':True},
                {'method':'GET','path':'/denied','q':0,'action':'deny','forward_attempted':False},
            ])))
            rows=b.read_records(path)
            self.assertEqual([x[1]['path'] for x in rows],['/safe/path'])


if __name__=='__main__':unittest.main()
