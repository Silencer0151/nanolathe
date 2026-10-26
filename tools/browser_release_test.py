"""Authored release listings for the browser publish plan; nothing calls gh."""
import unittest
from browser_release import ASSETS, KINDS, complete, plan, run_number


def release(tag, assets=ASSETS, draft=False, number=None):
    return {"id": number or 0, "tag_name": tag, "draft": draft, "assets": [{"name": name} for name in assets]}


class RunNumbers(unittest.TestCase):
    def test_only_the_prefixed_integer_suffix_counts(self):
        self.assertEqual(run_number("browser-12", "browser-"), 12)
        self.assertIsNone(run_number("browser-latest", "browser-"))
        self.assertIsNone(run_number("browser-", "browser-"))
        self.assertIsNone(run_number("v1.0", "browser-"))
        self.assertIsNone(run_number(None, "browser-"))

    def test_complete_needs_every_asset_on_a_published_release(self):
        self.assertTrue(complete(release("browser-1")))
        self.assertFalse(complete(release("browser-1", draft=True)))
        self.assertFalse(complete(release("browser-1", assets=ASSETS[:2])))


class Plan(unittest.TestCase):
    def test_keeps_the_newest_runs_and_prunes_the_rest(self):
        # API order is by creation, which need not follow the run number.
        releases = [release("browser-7"), release("browser-9"), release("browser-8"), release("browser-6"),
                    release("browser-latest", assets=()), release("browser-5", draft=True),
                    release("browser-4", assets=ASSETS[:1]), release("v1.0"), release("demo")]
        current, prune = plan(releases, "browser-", 10, 3)
        self.assertIsNone(current)
        self.assertEqual({r["tag_name"] for r in prune}, {"browser-7", "browser-6", "browser-latest", "browser-5", "browser-4"})

    def test_a_rerun_finds_its_complete_release(self):
        releases = [release("browser-10", number=42), release("browser-9")]
        current, prune = plan(releases, "browser-", 10, 3)
        self.assertEqual(current["id"], 42)
        self.assertEqual(prune, [])

    def test_a_stale_draft_of_this_run_is_pruned_after_publishing(self):
        current, prune = plan([release("browser-10", draft=True)], "browser-", 10, 3)
        self.assertIsNone(current)
        self.assertEqual([(r["tag_name"], r["draft"]) for r in prune], [("browser-10", True)])

    def test_a_published_incomplete_release_of_this_run_is_an_error(self):
        with self.assertRaises(RuntimeError):
            plan([release("browser-10", assets=ASSETS[:2])], "browser-", 10, 3)

    def test_native_and_browser_releases_never_prune_each_other(self):
        native = KINDS["native"][1]
        releases = [release("browser-8"), release("browser-9"), release("native-8", assets=native),
                    release("native-9", assets=native)]
        _, prune = plan(releases, "native-", 10, 2, native)
        self.assertEqual([r["tag_name"] for r in prune], ["native-8"])
        _, prune = plan(releases, "browser-", 10, 2)
        self.assertEqual([r["tag_name"] for r in prune], ["browser-8"])

    def test_keep_counts_this_run(self):
        releases = [release("browser-1"), release("browser-2")]
        self.assertEqual([r["tag_name"] for r in plan(releases, "browser-", 3, 1)[1]], ["browser-2", "browser-1"])
        self.assertEqual([r["tag_name"] for r in plan(releases, "browser-", 3, 2)[1]], ["browser-1"])


if __name__ == "__main__":
    unittest.main()
