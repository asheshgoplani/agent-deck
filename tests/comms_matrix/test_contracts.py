"""Guard against false-green result parsing and exception masking."""
import json
import unittest

from runner import check, decode_records


class ResultContractTests(unittest.TestCase):
    def test_expected_failure_only_accepts_enumerated_value(self):
        self.assertEqual(check('turns', 0, 1, [0])['status'], 'XFAIL')
        self.assertEqual(check('turns', 1, 1, [0])['status'], 'PASS')
        self.assertEqual(check('turns', 2, 1, [0])['status'], 'FAIL')
        self.assertEqual(check('turns', 'ssh failed', 1, [0])['status'], 'FAIL')
        self.assertEqual(check('turns', False, 1, [0])['status'], 'FAIL')

    def test_invalid_export_never_becomes_an_empty_success(self):
        for bad in ('not JSON', '{"records":{}}', '{"records":[{}]}'):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                decode_records(bad)

    def test_public_export_and_event_frames_preserve_record_identity(self):
        record = {'id': 'r1', 'key': 'turn:one', 'kind': 'turn', 'text': 'answer'}
        self.assertEqual(decode_records(json.dumps({'records': [record]})), [record])
        frames = [dict(cursor=i, data=record) for i in (1, 2)]
        # Decoder must not "fix" duplicates. The behavioral assertion sees both.
        self.assertEqual(decode_records('\n'.join(json.dumps(f) for f in frames)), [record, record])

    def test_cursor_record_export_envelope_preserves_record_identity(self):
        record = {'id': 'r1', 'key': 'turn:one', 'kind': 'turn', 'text': 'answer'}
        envelope = {'v': 1, 'records': [dict(cursor=i, record=record) for i in (1, 2)]}
        self.assertEqual(decode_records(json.dumps(envelope)), [record, record])
        envelope['records'] = [dict(cursor=1, record={})]
        with self.assertRaises(ValueError):
            decode_records(json.dumps(envelope))


if __name__ == '__main__':
    unittest.main()
