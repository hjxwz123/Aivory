"""Static regression tests for deterministic versioned Docker deployment."""

from pathlib import Path
import unittest


REPO_ROOT = Path(__file__).resolve().parents[2]
DEPLOY_ROOT = REPO_ROOT / "deploy"
PROD_COMPOSE = DEPLOY_ROOT / "docker-compose.prod.yml"
ENV_EXAMPLE = DEPLOY_ROOT / ".env.example"
SANDBOX_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "sandbox-image.yml"
APP_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "docker-images.yml"
SIDECAR_DOCKERFILE = REPO_ROOT / "sandbox-service" / "Dockerfile.sidecar"


class DeploymentImageTagsTest(unittest.TestCase):
    def test_sandbox_tags_inherit_app_tag_with_optional_override(self) -> None:
        compose = PROD_COMPOSE.read_text(encoding="utf-8")

        self.assertIn("aivory-app:${IMAGE_TAG:-latest}", compose)
        self.assertEqual(
            compose.count("${SANDBOX_IMAGE_TAG:-${IMAGE_TAG:-latest}}"),
            3,
            "sidecar, runtime environment, and keepalive must inherit one release tag",
        )

    def test_production_compose_cannot_build_over_a_pinned_release(self) -> None:
        production = PROD_COMPOSE.read_text(encoding="utf-8")

        self.assertNotIn("    build:\n", production)

    def test_environment_template_uses_one_release_tag_by_default(self) -> None:
        env = ENV_EXAMPLE.read_text(encoding="utf-8")

        self.assertIn("\nIMAGE_TAG=latest\n", env)
        self.assertNotIn("\nSANDBOX_IMAGE_TAG=", env)
        self.assertIn("\n# SANDBOX_IMAGE_TAG=latest\n", env)

    def test_future_release_tags_publish_sandbox_semver_images(self) -> None:
        workflow = SANDBOX_WORKFLOW.read_text(encoding="utf-8")

        self.assertIn("tags: ['v*.*.*']", workflow)
        self.assertIn("type=semver,pattern={{version}}", workflow)
        self.assertIn("type=semver,pattern={{major}}.{{minor}}", workflow)
        self.assertIn("type=semver,pattern={{major}}", workflow)

    def test_all_aivory_images_publish_amd64_and_arm64(self) -> None:
        expected = "PLATFORMS: linux/amd64,linux/arm64"

        self.assertIn(expected, APP_WORKFLOW.read_text(encoding="utf-8"))
        self.assertIn(expected, SANDBOX_WORKFLOW.read_text(encoding="utf-8"))

    def test_sidecar_selects_the_docker_cli_for_target_architecture(self) -> None:
        dockerfile = SIDECAR_DOCKERFILE.read_text(encoding="utf-8")

        self.assertIn("ARG TARGETARCH", dockerfile)
        self.assertIn('amd64) docker_cli_arch="x86_64"', dockerfile)
        self.assertIn('arm64) docker_cli_arch="aarch64"', dockerfile)
        self.assertNotIn("ARG DOCKER_CLI_ARCH", dockerfile)


if __name__ == "__main__":
    unittest.main()
