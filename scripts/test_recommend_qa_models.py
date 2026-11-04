import argparse
import contextlib
import io
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import recommend_qa_models as review


CONFIG = '''# Keep this header.
rolePrepend:
  polecat:
    - "openai/shared"
  qa:
    # Old recommendations.
    - "openai/shared" # existing primary
    - "vendor/old-reviewer"
  tester:
    - "openai/shared"

# Keep this boundary comment.
excludeModels:
  - "vendor/broken"
otherSetting: true
'''


def catalog_model(model_id="vendor/reviewer", **changes):
    model = {"id": model_id, "description": "Capable coding and agent model", "context_length": 200000,
             "supported_parameters": ["tools"], "pricing": {"prompt": "0.0000001", "completion": "0.0000005"}}
    model.update(changes)
    return model


class RecommendationTests(unittest.TestCase):
    def test_filters_capability_cost_exclusions_and_wrong_route(self):
        roles, excluded = review.read_policy(CONFIG)
        models = [catalog_model(), catalog_model("vendor/broken"), catalog_model("openai/shared"),
                  catalog_model("vendor/tiny-8b"), catalog_model("vendor/no-tools", supported_parameters=[]),
                  catalog_model("vendor/short", context_length=8192),
                  catalog_model("vendor/expensive", pricing={"prompt": "0.1", "completion": "0.2"}),
                  catalog_model("nvidia/openrouter-only-id"), catalog_model("vendor/incomplete", pricing={}),
                  catalog_model("vendor/batch:batch"), catalog_model("vendor/safety-70b")]
        args = argparse.Namespace(max_input=.5, max_output=1.5, min_context=128000, limit=8)
        result = review.shortlist(models, roles, excluded, args)
        self.assertEqual([model["id"] for model in result], ["vendor/reviewer"])

    def test_update_preserves_other_roles_comments_and_fallbacks(self):
        roles, _ = review.read_policy(CONFIG)
        updated = review.proposed_config(CONFIG, ["vendor/new", "vendor/new"], roles)
        new_roles, excluded = review.read_policy(updated)
        self.assertEqual(new_roles["qa"], ["vendor/new", "vendor/old-reviewer", "openai/shared"])
        for role in ("polecat", "tester"):
            self.assertEqual(new_roles[role], roles[role])
        self.assertEqual(excluded, {"vendor/broken"})
        self.assertTrue(updated.startswith("# Keep this header."))
        self.assertIn("# Keep this boundary comment.\nexcludeModels:", updated)
        self.assertTrue(updated.endswith("otherSetting: true\n"))

    def test_probe_rejects_empty_and_incorrect_answers(self):
        for content, want in [
            ('{"verdict":"FAIL","explanation":"inverted","actual_for_1_1":false,"expected_for_1_1":true}', True),
            ('{"verdict":"FAIL","explanation":"wrong","actual_for_1_1":true,"expected_for_1_1":true}', False),
            ("", False), ("not JSON", False),
        ]:
            with self.subTest(content=content), patch.object(review, "fetch_json", return_value={
                "choices": [{"message": {"content": content}}]
            }):
                passed, _ = review.probe_model(catalog_model(), "test-key")
                self.assertEqual(passed, want)

    def test_confirmation_and_backup(self):
        for answers, changed in [([""], False), (["1", "n"], False), (["1", "yes"], True)]:
            with self.subTest(answers=answers), tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / "models.yaml"
                path.write_text(CONFIG)
                with patch.object(review, "fetch_json", return_value={"data": [catalog_model()]}), \
                     patch.object(review.sys.stdin, "isatty", return_value=True), \
                     patch("builtins.input", side_effect=answers), contextlib.redirect_stdout(io.StringIO()) as output:
                    self.assertEqual(review.main(["--config", str(path)]), 0)
                backups = list(path.parent.glob("models.yaml.bak-*"))
                if changed:
                    self.assertNotEqual(path.read_text(), CONFIG)
                    self.assertIn("Proposed changes", output.getvalue())
                    self.assertEqual(len(backups), 1)
                    self.assertEqual(backups[0].read_text(), CONFIG)
                else:
                    self.assertEqual(path.read_text(), CONFIG)
                    self.assertEqual(backups, [])

    def test_noninteractive_does_not_write_or_prompt(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "models.yaml"
            path.write_text(CONFIG)
            with patch.object(review, "fetch_json", return_value={"data": [catalog_model()]}), \
                 patch.object(review.sys.stdin, "isatty", return_value=False), \
                 patch("builtins.input", side_effect=AssertionError("must not prompt")), \
                 contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(review.main(["--config", str(path)]), 0)
            self.assertEqual(path.read_text(), CONFIG)

    def test_refuses_concurrent_config_change(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "models.yaml"
            path.write_text(CONFIG + "concurrent: edit\n")
            with self.assertRaisesRegex(ValueError, "changed during review"):
                review.write_update(path, CONFIG, "must not overwrite")
            self.assertEqual(path.read_text(), CONFIG + "concurrent: edit\n")
            self.assertEqual(list(path.parent.glob("models.yaml.bak-*")), [])


if __name__ == "__main__":
    unittest.main()
