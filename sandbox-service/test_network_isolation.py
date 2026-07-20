"""Static regression tests for the runner container's network boundary.

These tests intentionally parse app.py instead of importing it, so they run
with the Python standard library alone and do not require FastAPI or Docker.
"""

import ast
from pathlib import Path
import unittest


APP_PATH = Path(__file__).with_name("app.py")
REPO_ROOT = APP_PATH.parent.parent
DEPLOYMENT_CONFIGS = (
    APP_PATH.parent / "Dockerfile.sidecar",
    APP_PATH.parent / "docker-compose.yml",
    REPO_ROOT / "deploy" / "docker-compose.prod.yml",
    REPO_ROOT / "deploy" / ".env.example",
)


class RunnerNetworkIsolationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.source = APP_PATH.read_text(encoding="utf-8")
        cls.tree = ast.parse(cls.source, filename=str(APP_PATH))

    def test_network_cannot_be_overridden_by_environment(self) -> None:
        self.assertNotIn(
            "SANDBOX_NETWORK",
            self.source,
            "runner networking must not be configurable through the environment",
        )

    def test_deployment_configs_do_not_offer_network_escape_hatch(self) -> None:
        for path in DEPLOYMENT_CONFIGS:
            with self.subTest(path=path.relative_to(REPO_ROOT)):
                self.assertNotIn(
                    "SANDBOX_NETWORK",
                    path.read_text(encoding="utf-8"),
                    "deployment config must not advertise a runner network override",
                )

    def test_create_session_forces_docker_network_none(self) -> None:
        create_session = next(
            node
            for node in self.tree.body
            if isinstance(node, ast.FunctionDef) and node.name == "create_session"
        )
        network_values = []
        for node in ast.walk(create_session):
            if not isinstance(node, (ast.List, ast.Tuple)):
                continue
            values = [
                item.value if isinstance(item, ast.Constant) and isinstance(item.value, str) else None
                for item in node.elts
            ]
            for index, value in enumerate(values[:-1]):
                if value == "--network":
                    network_values.append(values[index + 1])

        self.assertEqual(
            network_values,
            ["none"],
            "create_session must pass exactly one literal '--network none' pair to docker run",
        )


if __name__ == "__main__":
    unittest.main()
