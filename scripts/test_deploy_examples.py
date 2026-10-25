"""Deployment examples must keep both listeners off public interfaces (FIN-286)."""
from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[1]


class DeployExamplesTests(unittest.TestCase):
    def test_systemd_unit_binds_listeners_to_loopback(self):
        unit = (ROOT / "deploy" / "systemd" / "bank-bff.service").read_text()
        environment = dict(re.findall(r"^Environment=([A-Z_]+)=(\S+)$", unit, re.MULTILINE))
        for key in ("PUBLIC_ADDR", "HTTP_ADDR"):
            with self.subTest(key=key):
                self.assertIn(key, environment, "the default address listens on all interfaces")
                self.assertTrue(environment[key].startswith("127.0.0.1:"), environment[key])

    def test_compose_publishes_ports_only_on_loopback(self):
        compose = (ROOT / "compose.yaml").read_text()
        ports = re.findall(r'^\s*-\s*"([^"]+)"\s*$', compose, re.MULTILINE)
        self.assertTrue(ports)
        for mapping in ports:
            with self.subTest(mapping=mapping):
                self.assertTrue(mapping.startswith("127.0.0.1:"), mapping)


if __name__ == "__main__":
    unittest.main()
