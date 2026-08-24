"""Static regressions for non-root workspace archive restoration.

The sidecar itself is intentionally not imported here: doing so would require
FastAPI and Docker in the unit-test environment. Parsing the restore function
still locks down the security-sensitive tar flags used in production.
"""

import ast
from pathlib import Path
import unittest


REPO_ROOT = Path(__file__).resolve().parents[2]
APP_PATH = REPO_ROOT / "sandbox-service" / "app.py"


class WorkspaceRestoreTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.tree = ast.parse(APP_PATH.read_text(encoding="utf-8"), filename=str(APP_PATH))

    def test_restore_preserves_existing_tmpfs_directory_metadata(self) -> None:
        restore = next(
            node
            for node in self.tree.body
            if isinstance(node, ast.FunctionDef) and node.name == "_restore_workspace"
        )
        docker_args: set[str] = set()
        for node in ast.walk(restore):
            if not isinstance(node, ast.List):
                continue
            values = {
                item.value
                for item in node.elts
                if isinstance(item, ast.Constant) and isinstance(item.value, str)
            }
            if "--no-same-owner" in values:
                docker_args = values
                break

        self.assertIn("--no-same-owner", docker_args)
        self.assertIn(
            "--no-overwrite-dir",
            docker_args,
            "restoring as uid 1000 must preserve existing child-directory metadata",
        )
        self.assertIn(
            "--strip-components=1",
            docker_args,
            "the archive's leading ./ entry must not chmod/utime the /workspace mount",
        )


if __name__ == "__main__":
    unittest.main()
