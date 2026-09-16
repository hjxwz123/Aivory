import importlib.util
import os
import stat
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("aivory_updater", Path(__file__).with_name("updater.py"))
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader
SPEC.loader.exec_module(MODULE)


class UpdateManagerTest(unittest.TestCase):
    def make_manager(self):
        temp = tempfile.TemporaryDirectory()
        root = Path(temp.name)
        compose = root / "compose.yml"
        compose.write_text("services: {}\n", encoding="utf-8")
        env = root / ".env"
        env.write_text("IMAGE_TAG=2.4.7\nKEEP=this\n", encoding="utf-8")
        values = {
            "AIVORY_UPDATER_STATE_DIR": str(root / "state"),
            "AIVORY_UPDATER_TOKEN_FILE": str(root / "shared" / "token"),
            "AIVORY_UPDATER_COMPOSE_FILE": str(compose),
            "AIVORY_UPDATER_ENV_FILE": str(env),
        }
        with patch.dict(os.environ, values, clear=False):
            manager = MODULE.UpdateManager(MODULE.Settings())
        return temp, manager, env

    def test_rejects_non_semantic_or_injected_version(self):
        temp, manager, _ = self.make_manager()
        self.addCleanup(temp.cleanup)
        for version in ("latest", "v2.4.8", "2.4.8+build", "2.4.8-rc.01", "2.04.8", "2.4.8;id", "2.4"):
            with self.assertRaises(ValueError):
                manager.start(version)

    def test_accepts_prerelease_version(self):
        temp, manager, _ = self.make_manager()
        self.addCleanup(temp.cleanup)
        with patch.object(manager, "_run", return_value=None):
            job, started = manager.start("2.4.9-beta.2")
        self.assertTrue(started)
        self.assertEqual(job["version"], "2.4.9-beta.2")

    def test_env_update_preserves_other_values(self):
        temp, manager, env = self.make_manager()
        self.addCleanup(temp.cleanup)
        previous = manager._write_env_value("APP_IMAGE_TAG", "2.4.8")
        self.assertIsNone(previous)
        self.assertEqual(env.read_text(encoding="utf-8"), "IMAGE_TAG=2.4.7\nKEEP=this\nAPP_IMAGE_TAG=2.4.8\n")
        manager._restore_env_value("APP_IMAGE_TAG", previous)
        self.assertEqual(env.read_text(encoding="utf-8"), "IMAGE_TAG=2.4.7\nKEEP=this\n")

    def test_env_update_preserves_file_permissions(self):
        temp, manager, env = self.make_manager()
        self.addCleanup(temp.cleanup)
        os.chmod(env, 0o640)
        manager._write_env_value("APP_IMAGE_TAG", "2.4.8")
        self.assertEqual(stat.S_IMODE(env.stat().st_mode), 0o640)

    def test_single_flight(self):
        temp, manager, _ = self.make_manager()
        self.addCleanup(temp.cleanup)
        with patch.object(manager, "_run", return_value=None):
            first, started = manager.start("2.4.8")
            second, started_again = manager.start("2.4.9")
        self.assertTrue(started)
        self.assertFalse(started_again)
        self.assertEqual(first["id"], second["id"])

    def test_compose_uses_real_host_data_mount(self):
        temp, manager, _ = self.make_manager()
        self.addCleanup(temp.cleanup)
        calls = []

        def command(args, timeout=600, env=None):
            calls.append((args, env))
            if args[:3] == ["docker", "inspect", "--format"]:
                return '[{"Source":"/srv/aivory/data","Destination":"/shared"}]'
            return ""

        with patch.object(manager, "_command", side_effect=command):
            manager._compose("ps", "-q", "app")
        self.assertEqual(calls[-1][1]["DATA_DIR"], "/srv/aivory/data")


if __name__ == "__main__":
    unittest.main()
