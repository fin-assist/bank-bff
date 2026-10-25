"""Deployment examples must keep both listeners off public interfaces (FIN-286).

Both checks are allowlists: anything the parsers do not explicitly understand fails
closed, so a new directive, key or YAML form cannot silently weaken the examples.
"""
from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[1]
UNIT = ROOT / "deploy" / "systemd" / "bank-bff.service"

# Security-relevant [Service] directives: every assignment, in order, must be exactly this.
# Each of them changes the process environment, the command or the IP firewall.
EXACT_SERVICE = {
    "ExecStart": ["/opt/finassist/bank-bff/bank-bff serve"],
    "EnvironmentFile": ["/etc/finassist/bank-bff/env"],
    "Environment": ["ENV=stage", "PUBLIC_ADDR=127.0.0.1:8080", "HTTP_ADDR=127.0.0.1:9091"],
    "IPAddressAllow": ["localhost"],
    "IPAddressDeny": ["any"],
}
# Hardening and lifecycle directives that cannot change listener addresses or filters.
OTHER_SERVICE = {
    "Type", "DynamicUser", "Restart", "RestartSec", "TimeoutStopSec", "KillSignal",
    "NoNewPrivileges", "ProtectSystem", "ProtectHome", "PrivateTmp", "PrivateDevices",
    "ProtectKernelTunables", "ProtectKernelModules", "ProtectControlGroups",
    "RestrictAddressFamilies", "RestrictNamespaces", "LockPersonality",
    "MemoryDenyWriteExecute", "SystemCallArchitectures", "CapabilityBoundingSet",
}
# systemd ignores [Service] keys in these sections, so their content cannot weaken the unit.
OTHER_SECTIONS = {"Unit", "Install"}


def logical_lines(unit):
    """Join continuation lines as systemd.syntax(7) does.

    A line ending in an odd number of backslashes continues on the next line (the
    backslash becomes a space); comment lines inside a continuation are skipped.
    """
    result, pending = [], None
    for line in unit.splitlines():
        if pending is not None and line.lstrip().startswith(("#", ";")):
            continue
        trailing = len(line) - len(line.rstrip("\\"))
        if trailing % 2:
            pending = (pending or "") + line[:-1] + " "
            continue
        result.append((pending or "") + line)
        pending = None
    if pending is not None:
        result.append(pending)
    return result


def service_directives(unit):
    """Return [Service] directives as (key, value) with whitespace stripped as systemd does."""
    directives, section = [], None
    for line in logical_lines(unit):
        stripped = line.strip()
        if not stripped or stripped.startswith(("#", ";")):
            continue
        if stripped.startswith("[") and stripped.endswith("]"):
            section = stripped[1:-1]
            if section != "Service" and section not in OTHER_SECTIONS:
                raise ValueError(f"unknown section [{section}]")
            continue
        key, sep, value = stripped.partition("=")
        if not sep:
            raise ValueError(f"not a directive: {stripped!r}")
        if section is None:
            raise ValueError(f"directive outside a section: {stripped!r}")
        if section == "Service":
            directives.append((key.strip(), value.strip()))
    return directives


def check_unit(unit):
    """Reject a unit whose [Service] section is not exactly the allowlisted one."""
    exact = {key: [] for key in EXACT_SERVICE}
    for key, value in service_directives(unit):
        if key in exact:
            exact[key].append(value)
        elif key not in OTHER_SERVICE:
            raise AssertionError(f"[Service] directive {key}= is not allowlisted")
    for key, expected in EXACT_SERVICE.items():
        if exact[key] != expected:
            raise AssertionError(f"{key}= must be exactly {expected}, got {exact[key]}")


KEY = re.compile(r"^([A-Za-z0-9_.-]+):(?:[ ]+(.*))?$")
PLAIN_FORBIDDEN = re.compile(r"""^[{\[&*!|>@%`'"]|\s#|:\s|^-""")
STRICT_PORT = re.compile(r'^"127\.0\.0\.1:(\d+):(\d+)"$')
TOP_KEYS = {"services"}
SERVICE_KEYS = {"build", "environment", "ports", "command"}


def scalar(value):
    """Accept a plain or double-quoted single-line scalar, nothing else."""
    if re.fullmatch(r'"[^"\\]*"', value):
        return value
    if not value or PLAIN_FORBIDDEN.search(value):
        raise ValueError(f"unsupported YAML scalar {value!r}")
    return value


def indent(line):
    return len(line) - len(line.lstrip(" "))


def parse_yaml(text):
    """Parse the tiny block-YAML subset used by compose.yaml; fail closed on the rest."""
    lines = [line.rstrip() for line in text.splitlines()
             if line.strip() and not line.lstrip().startswith("#")]
    if any("\t" in line for line in lines):
        raise ValueError("tabs in compose.yaml")

    def block(i, depth):
        if lines[i].lstrip().startswith("-"):
            return sequence(i, depth)
        result = {}
        while i < len(lines) and indent(lines[i]) == depth and not lines[i].lstrip().startswith("-"):
            match = KEY.match(lines[i].strip())
            if not match:
                raise ValueError(f"unsupported YAML line {lines[i].strip()!r}")
            key, value = match.groups()
            if key in result:
                raise ValueError(f"duplicate key {key}")
            i += 1
            if value:
                result[key] = scalar(value)
            elif i < len(lines) and (indent(lines[i]) > depth or (
                    indent(lines[i]) == depth and lines[i].lstrip().startswith("-"))):
                result[key], i = block(i, indent(lines[i]))
            else:
                raise ValueError(f"empty value for {key}")
        if i < len(lines) and indent(lines[i]) > depth:
            raise ValueError(f"unexpected indentation {lines[i].strip()!r}")
        return result, i

    def sequence(i, depth):
        items = []
        while i < len(lines) and indent(lines[i]) == depth and lines[i].lstrip().startswith("-"):
            match = re.match(r"^-[ ]+(.+)$", lines[i].strip())
            if not match:
                raise ValueError(f"unsupported sequence item {lines[i].strip()!r}")
            items.append(scalar(match.group(1)))
            i += 1
        if i < len(lines) and indent(lines[i]) > depth:
            raise ValueError(f"unexpected indentation {lines[i].strip()!r}")
        return items, i

    if not lines or indent(lines[0]) != 0:
        raise ValueError("compose.yaml must start with a top-level mapping")
    document, i = block(0, 0)
    if i != len(lines) or not isinstance(document, dict):
        raise ValueError(f"unsupported YAML line {lines[i].strip()!r}")
    return document


def published_ports(compose):
    """Return (published, target) of every port of an allowlisted compose document.

    Only the top-level ``services`` key and the service keys in SERVICE_KEYS are allowed,
    so include, extends, networks, network_mode and any other way to pull or widen
    configuration fail. Every port is the strict form ``"127.0.0.1:N:N"``.
    """
    document = parse_yaml(compose)
    if set(document) - TOP_KEYS or not isinstance(document.get("services"), dict):
        raise ValueError(f"top-level keys must be {sorted(TOP_KEYS)}: {sorted(document)}")
    entries = []
    for name, service in document["services"].items():
        if not isinstance(service, dict) or set(service) - SERVICE_KEYS:
            raise ValueError(f"service {name} keys must be within {sorted(SERVICE_KEYS)}")
        if "environment" in service and not isinstance(service["environment"], dict):
            raise ValueError("environment must be a mapping")
        ports = service.get("ports", [])
        if not isinstance(ports, list):
            raise ValueError("ports must be a block sequence")
        for port in ports:
            match = STRICT_PORT.match(port)
            if not match:
                raise ValueError(f'ports entry must be "127.0.0.1:N:N": {port!r}')
            entries.append(match.groups())
    return entries


def compose_files(root):
    """Return files in root that docker compose could load as configuration."""
    return sorted(path.name for path in root.iterdir()
                  if path.is_file() and re.search(r"(?i)compose.*\.ya?ml$", path.name))


class DeployExamplesTests(unittest.TestCase):
    def test_systemd_unit_is_allowlisted(self):
        check_unit(UNIT.read_text())

    def test_systemd_bypass_forms_are_rejected(self):
        unit = UNIT.read_text()
        self.assertEqual(unit.count("\n[Install]\n"), 1)

        def service(extra):
            return unit + "[Service]\n" + extra + "\n"

        moved = unit
        for line in ("Environment=PUBLIC_ADDR=127.0.0.1:8080", "Environment=HTTP_ADDR=127.0.0.1:9091",
                     "IPAddressAllow=localhost", "IPAddressDeny=any"):
            self.assertIn(line + "\n", unit)
            moved = moved.replace(line + "\n", "") + line + "\n"
        for mutated in [
            unit.replace("Environment=HTTP_ADDR=127.0.0.1:9091\n", ""),
            service('Environment="HTTP_ADDR=0.0.0.0:9091"'),
            service("Environment=ENV=stage PUBLIC_ADDR=:8080"),
            service("Environment="),
            service("Environment = HTTP_ADDR=0.0.0.0:9091"),
            unit.replace("IPAddressDeny=any\n", ""),
            service("IPAddressDeny="),
            service("IPAddressDeny = "),
            unit.replace("IPAddressAllow=localhost\n", "IPAddressAllow=any\n"),
            service("IPAddressAllow=0.0.0.0/0"),
            service("IPAddressAllow = any"),
            service("UnsetEnvironment=HTTP_ADDR"),
            service("UnsetEnvironment=PUBLIC_ADDR=127.0.0.1:8080"),
            service("EnvironmentFile=-/etc/default/bank-bff"),
            service("ExecStart="),
            unit.replace("ExecStart=/opt/finassist/bank-bff/bank-bff serve",
                         "ExecStart=/usr/bin/env HTTP_ADDR=0.0.0.0:9091 /opt/finassist/bank-bff/bank-bff serve"),
            service("ExecStartPre=/bin/true"),
            service("PassEnvironment=HTTP_ADDR"),
            moved,
            unit.replace("Environment=HTTP_ADDR=", "Nice=0 \\\nEnvironment=HTTP_ADDR="),
            unit.replace("IPAddressDeny=any\n", "Nice=0 \\\n# swallowed comment\nIPAddressDeny=any\n"),
            unit + "[X-Extra]\nEnvironment=HTTP_ADDR=0.0.0.0:9091\n",
            "Environment=HTTP_ADDR=0.0.0.0:9091\n" + unit,
        ]:
            with self.subTest(mutated=mutated[-160:]):
                self.assertNotEqual(mutated, unit)
                with self.assertRaises((AssertionError, ValueError)):
                    check_unit(mutated)

    def test_systemd_ignored_sections_and_syntax_are_accepted(self):
        unit = UNIT.read_text()
        # systemd ignores service directives outside [Service].
        check_unit(unit + "[Unit]\nIPAddressAllow=any\n")
        check_unit(unit + 'Environment="HTTP_ADDR=0.0.0.0:9091"\n')
        # An even number of trailing backslashes does not continue the line.
        self.assertEqual(logical_lines("A=1 \\\\\nB=2\n"), ["A=1 \\\\", "B=2"])
        self.assertEqual(logical_lines("A=1 \\\n; note\nB=2\n"), ["A=1  B=2"])

    def test_compose_is_the_only_compose_file(self):
        self.assertEqual(compose_files(ROOT), ["compose.yaml"])

    def test_compose_publishes_ports_only_on_loopback(self):
        entries = published_ports((ROOT / "compose.yaml").read_text())
        self.assertEqual(sorted(entries), [("8080", "8080"), ("9091", "9091")])

    def test_compose_allowed_forms_are_accepted(self):
        head = ('services:\n  bff:\n    build: .\n    command:\n      - "serve"\n'
                '    environment:\n      ENV: dev\n    ports:\n      - "127.0.0.1:8080:8080"\n')
        self.assertEqual(published_ports(head), [("8080", "8080")])
        self.assertEqual(published_ports(head + '    # note\n  other:\n    ports:\n    - "127.0.0.1:1:2"\n'),
                         [("8080", "8080"), ("1", "2")])

    def test_compose_bypass_forms_are_rejected(self):
        head = 'services:\n  bff:\n    ports:\n      - "127.0.0.1:8080:8080"\n'
        for tail in ["      - 9091:9091", "      - '9091:9091'", '      - "9091:9091"',
                     "      - '127.0.0.1:9091:9091'", "      - 127.0.0.1:9091:9091",
                     '      - "0.0.0.0:9091:9091"', '      - "[::]:9091:9091"', "      - 9091",
                     "      - target: 9091\n        published: 9091",
                     "      - {target: 9091, published: 9091}", "      - *operational",
                     '      - "127.0.0.1:9091:9091" # comment',
                     "    network_mode: host", '    "network_mode": host', "    'network_mode': host",
                     "    networks:\n      - hostnet", "    extends:\n      file: base.yaml\n      service: base",
                     "    <<: *base", "  other:\n    ports:\n      - 9091:9091",
                     '  other:\n    "ports":\n      - 9091:9091', "  other:\n    ports:\n    - 9091:9091",
                     '  other: {image: x, ports: ["9091:9091"]}', "  other:\n    image: x",
                     "    ports:\n      - \"127.0.0.1:1:1\""]:
            with self.subTest(tail=tail):
                with self.assertRaises(ValueError):
                    published_ports(head + tail + "\n")
        for compose in ['services:\n  bff:\n    ports: ["127.0.0.1:9091:9091"]\n',
                        "include:\n  - other.yaml\n" + head,
                        head + "networks:\n  hostnet:\n    name: host\n",
                        head + "x-base: &base\n  image: x\n",
                        "  " + head, ""]:
            with self.subTest(compose=compose):
                with self.assertRaises(ValueError):
                    published_ports(compose)

    def test_extra_compose_files_are_detected(self):
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "compose.yaml").write_text("services: {}\n")
            for name in ("compose.override.yaml", "compose.override.yml", "docker-compose.yml"):
                (root / name).write_text('services:\n  bff:\n    ports:\n      - "9091:9091"\n')
            self.assertEqual(compose_files(root), ["compose.override.yaml", "compose.override.yml",
                                                   "compose.yaml", "docker-compose.yml"])


if __name__ == "__main__":
    unittest.main()
