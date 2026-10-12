import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).resolve().parents[1] / "xy2api_upgrade_preflight.py"
SPEC = importlib.util.spec_from_file_location("xy2api_upgrade_preflight", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
sys.modules[SPEC.name] = MODULE
SPEC.loader.exec_module(MODULE)


class UpgradePreflightTest(unittest.TestCase):
    def base_env(self):
        return {
            "POSTGRES_DB": "sub2api",
            "POSTGRES_PASSWORD": "db-secret",
            "REDIS_DB": "0",
            "DASHBOARD_CACHE_KEY_PREFIX": "sub2api:",
            "JWT_SECRET": "jwt-secret",
            "TOTP_ENCRYPTION_KEY": "totp-secret",
            "APP_DATA_VOLUME_NAME": "deploy_sub2api_data",
        }

    def test_accepts_explicit_legacy_identity(self):
        with tempfile.TemporaryDirectory() as temp:
            findings = MODULE.evaluate(
                self.base_env(),
                ["deploy_sub2api_data"],
                Path(temp),
                None,
                "sub2api",
                "0",
                "sub2api:",
            )
        self.assertFalse([item for item in findings if item.level == "error"])

    def test_blocks_fresh_install_defaults(self):
        env = self.base_env()
        env["POSTGRES_DB"] = "xy2api"
        env["APP_DATA_VOLUME_NAME"] = "xy2api_data"
        with tempfile.TemporaryDirectory() as temp:
            findings = MODULE.evaluate(
                env,
                ["deploy_sub2api_data"],
                Path(temp),
                None,
                "sub2api",
                "0",
                "sub2api:",
            )
        codes = {item.code for item in findings if item.level == "error"}
        self.assertIn("postgres-db-changed", codes)
        self.assertIn("legacy-volume-not-selected", codes)

    def test_blocks_ambiguous_legacy_volumes(self):
        env = self.base_env()
        env["APP_DATA_VOLUME_NAME"] = "xy2api_data"
        with tempfile.TemporaryDirectory() as temp:
            findings = MODULE.evaluate(
                env,
                ["deploy_sub2api_data", "sub2api_sub2api_data"],
                Path(temp),
                None,
                "sub2api",
                "0",
                "sub2api:",
            )
        self.assertIn("legacy-volume-ambiguous", {item.code for item in findings})

    def test_legacy_bind_mount_does_not_require_named_volume(self):
        env = self.base_env()
        env["APP_DATA_VOLUME_NAME"] = "xy2api_data"
        with tempfile.TemporaryDirectory() as temp:
            compose = Path(temp) / "compose.yml"
            compose.write_text("- /var/lib/sub2api/production-app:/app/data\n", encoding="utf-8")
            findings = MODULE.evaluate(env, [], Path(temp), compose, "sub2api", "0", "sub2api:")
        self.assertFalse([item for item in findings if item.level == "error"])
        self.assertIn("legacy-bind-mount", {item.code for item in findings})


class SchedulingPreflightTest(unittest.TestCase):
    def inventory(self):
        return dict(controlled_mode=True, cutover_acknowledged=True,
                    legacy_rule_count=8, zero_weight_count=2, skipped_rule_count=1,
                    skipped_rules_reviewed=True, legacy_clients_acknowledged=True)

    def test_requires_explicit_cutover_and_client_review(self):
        inventory = self.inventory()
        inventory.update(cutover_acknowledged=False, legacy_clients_acknowledged=False)
        codes = {f.code for f in MODULE.evaluate_scheduling(inventory) if f.level == "error"}
        self.assertEqual(codes, {"controlled-cutover-unacknowledged", "retired-scheduling-clients"})

    def test_rejects_unreviewed_skipped_rules(self):
        inventory = self.inventory()
        inventory["skipped_rules_reviewed"] = False
        self.assertIn("skipped-scheduling-rules", {f.code for f in MODULE.evaluate_scheduling(inventory)})

    def test_accepts_review_and_explains_inactive_zero_weights(self):
        findings = MODULE.evaluate_scheduling(self.inventory())
        self.assertFalse([f for f in findings if f.level == "error"])
        self.assertIn("zero-weight-import", {f.code for f in findings})

    def test_rejects_missing_or_invalid_inventory(self):
        for inventory in ({}, None, {**self.inventory(), "legacy_rule_count": True},
                          {**self.inventory(), "zero_weight_count": 99}):
            self.assertEqual(MODULE.evaluate_scheduling(inventory)[0].code, "scheduling-inventory-invalid")


if __name__ == "__main__":
    unittest.main()
