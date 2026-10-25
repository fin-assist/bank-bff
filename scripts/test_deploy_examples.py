"""Deployment examples must keep both listeners off public interfaces (FIN-286)."""
from pathlib import Path
import re
import shlex
import unittest


ROOT = Path(__file__).resolve().parents[1]
STRICT_PORT = re.compile(r'^-[ ]"127\.0\.0\.1:(\d+):(\d+)"$')


QUOTED_KEY = re.compile(r"""^\s*(?:-\s+)?["'][^"']*["']\s*:""")
UNSUPPORTED = re.compile(r"[{}&]|(?:^|[\s:\[,])\*|<<|^\s*(?:-\s+)?\?")


def published_ports(compose):
    """Return (published, target) of every ports entry of compose.yaml.

    Only one strict form is allowed inside a block ``ports:`` sequence:
    ``- "127.0.0.1:N:N"``, indented under ``ports:`` or at the same indent.
    Everything this small parser does not understand fails closed instead of being
    skipped: other port forms, flow sequences after ``ports:``, ``network_mode``,
    quoted keys, flow mappings, anchors, aliases, merge and complex keys. Quoted
    items of other lists (``command``) are ignored.
    """
    lines = [line for line in compose.splitlines()
             if line.strip() and not line.lstrip().startswith("#")]
    if any("\t" in line for line in lines):
        raise ValueError("tabs in compose.yaml")
    entries, i = [], 0
    while i < len(lines):
        line = lines[i]
        i += 1
        if QUOTED_KEY.match(line) or UNSUPPORTED.search(line):
            raise ValueError(f"unsupported YAML form: {line.strip()!r}")
        if re.match(r"^\s*network_mode\s*:", line):
            raise ValueError("network_mode bypasses port publishing")
        match = re.match(r"^(\s*)ports\s*:\s*(.*)$", line)
        if not match:
            continue
        if match.group(2):
            raise ValueError(f"ports must use the block form: {match.group(2)!r}")
        base = len(match.group(1))
        while i < len(lines):
            depth = len(lines[i]) - len(lines[i].lstrip(" "))
            if depth < base or (depth == base and not lines[i].lstrip().startswith("-")):
                break
            item = STRICT_PORT.match(lines[i].strip())
            if not item:
                raise ValueError(f'ports entry must be "127.0.0.1:N:N": {lines[i].strip()!r}')
            entries.append(item.groups())
            i += 1
    return entries


class DeployExamplesTests(unittest.TestCase):
    def assert_unit_safe(self, unit):
        """Apply directives in order with systemd semantics.

        The parser strips whitespace around the key and the value; the last
        Environment= assignment wins and an empty one resets the list; an empty
        IPAddressAllow=/IPAddressDeny= resets that list (systemd.exec(5),
        systemd.resource-control(5)).
        """
        environment, settings = {}, {}
        for line in unit.splitlines():
            if line.lstrip().startswith(("#", ";")):
                continue
            key, sep, value = line.partition("=")
            if not sep:
                continue
            key, value = key.strip(), value.strip()
            if key == "Environment":
                if not value:
                    environment.clear()
                for assignment in shlex.split(value):
                    name, _, address = assignment.partition("=")
                    environment[name] = address
            elif key in ("IPAddressAllow", "IPAddressDeny"):
                if not value:
                    settings[key] = []
                else:
                    settings.setdefault(key, []).extend(value.split())
        for name in ("PUBLIC_ADDR", "HTTP_ADDR"):
            if not environment.get(name, "").startswith("127.0.0.1:"):
                raise AssertionError(f"{name}={environment.get(name)!r} is not loopback")
        if "any" not in settings.get("IPAddressDeny", []):
            raise AssertionError(f"IPAddressDeny must include any: {settings.get('IPAddressDeny')}")
        if settings.get("IPAddressAllow") != ["localhost"]:
            raise AssertionError(f"IPAddressAllow must be exactly localhost: {settings.get('IPAddressAllow')}")

    def test_systemd_unit_binds_listeners_to_loopback(self):
        self.assert_unit_safe((ROOT / "deploy" / "systemd" / "bank-bff.service").read_text())

    def test_systemd_unit_overrides_are_rejected(self):
        unit = (ROOT / "deploy" / "systemd" / "bank-bff.service").read_text()
        for mutated in [
            unit.replace("Environment=HTTP_ADDR=127.0.0.1:9091\n", ""),
            unit + 'Environment="HTTP_ADDR=0.0.0.0:9091"\n',
            unit + "Environment=ENV=stage PUBLIC_ADDR=:8080\n",
            unit.replace("IPAddressDeny=any\n", ""),
            unit + "IPAddressDeny=\n",
            unit.replace("IPAddressAllow=localhost\n", "IPAddressAllow=any\n"),
            unit + "IPAddressAllow=0.0.0.0/0\n",
            unit + "Environment=\n",
            unit + "Environment = HTTP_ADDR=0.0.0.0:9091\n",
            unit + "IPAddressAllow = any\n",
            unit + "IPAddressDeny = \n",
        ]:
            with self.subTest(tail=mutated[-60:]):
                self.assertNotEqual(mutated, unit)
                with self.assertRaises(AssertionError):
                    self.assert_unit_safe(mutated)

    def test_compose_publishes_ports_only_on_loopback(self):
        entries = published_ports((ROOT / "compose.yaml").read_text())
        self.assertEqual(sorted(entries), [("8080", "8080"), ("9091", "9091")])

    def test_compose_bypass_forms_are_rejected(self):
        head = 'services:\n  bff:\n    command:\n      - "serve"\n    ports:\n      - "127.0.0.1:8080:8080"\n'
        self.assertEqual(published_ports(head), [("8080", "8080")])
        for tail in ["      - 9091:9091", "      - '9091:9091'", '      - "9091:9091"',
                     "      - '127.0.0.1:9091:9091'", "      - 127.0.0.1:9091:9091",
                     '      - "0.0.0.0:9091:9091"', '      - "[::]:9091:9091"', "      - 9091",
                     "      - target: 9091\n        published: 9091",
                     "      - target: 9091\n        host_ip: 127.0.0.1",
                     "      - {target: 9091, published: 9091}", "      - *operational",
                     '      - "127.0.0.1:9091:9091" # comment']:
            with self.subTest(tail=tail):
                with self.assertRaises(ValueError):
                    published_ports(head + tail + "\n")
        for compose in ['services:\n  bff:\n    ports: ["127.0.0.1:9091:9091"]\n',
                        "services:\n  bff:\n    ports: [9091:9091]\n",
                        "services:\n  bff:\n    network_mode: host\n",
                        head + "    network_mode: host\n",
                        head + "  other:\n    ports:\n      - 9091:9091\n",
                        head + '    "network_mode": host\n',
                        head + "    'network_mode': host\n",
                        head + '  other:\n    "ports":\n      - 9091:9091\n',
                        head + "  other:\n    ports:\n    - 9091:9091\n",
                        head + '  other: {image: x, ports: ["9091:9091"]}\n',
                        head + "  other:\n    <<: *base\n"]:
            with self.subTest(compose=compose):
                with self.assertRaises(ValueError):
                    published_ports(compose)


if __name__ == "__main__":
    unittest.main()
