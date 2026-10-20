import unittest
from instant_playback_gate import distribution, evaluate


class GateEvidenceTest(unittest.TestCase):
    def test_nearest_rank(self):
        result = distribution([i * 1000000 for i in range(1, 101)])
        self.assertEqual([result[k] for k in ["p50_ms", "p95_ms", "p99_ms", "max_ms"]], [50, 95, 99, 100])

    def test_missing_matrix_fails_closed(self):
        self.assertEqual(evaluate([], "deterministic")["status"], "FAIL")
        self.assertFalse(evaluate([], "real")["release_approved"])

    def test_failed_warmup_with_null_samples_produces_failure_report(self):
        record={"scenario":{"Name":"playlist-500","Mode":"warm","Guilds":1},"status":"FAIL",
                "environment":"real-providers-discord-dave","monotonic_ns":None,"errors":["metadata failed"]}
        result=evaluate([record],"real")
        self.assertEqual(result["scenarios"][0]["ttfa"]["n"],0)
        self.assertEqual(result["status"],"FAIL")
        self.assertTrue(any("provider/transport" in e for e in result["errors"]))

    def test_simulated_udp_cannot_approve_real_voice(self):
        sample = {key: 1 for key in ["ack", "metadata", "first_resolved", "extraction", "ffmpeg_started", "opus"]}
        sample.update(received=0, transport_sent=1000000)
        record = {"scenario": {"Name": "youtube-single", "Mode": "warm"}, "status": "PASS",
                  "environment": "deterministic-dispatch-real-ffmpeg-controlled-ytdlp-simulated-udp",
                  "monotonic_ns": [sample] * 100}
        report = evaluate([record], "real")
        self.assertFalse(report["release_approved"])
        self.assertTrue(any("environment" in e for e in report["errors"]))


if __name__ == "__main__":
    unittest.main()
