"""Run: python3 scripts/coderbuzz/group_agents_test.py"""
import json
import os
import sqlite3
import subprocess
import tempfile
import unittest

TOOL = os.path.join(os.path.dirname(os.path.abspath(__file__)), "group-agents")
G1, G2 = "120000000000000001@g.us", "120000000000000002@g.us"


class GroupAgentsTest(unittest.TestCase):
    def setUp(self):
        self.home = tempfile.mkdtemp()
        os.makedirs(os.path.join(self.home, ".picoclaw/workspace/whatsapp"))
        cfg = {
            "agents": {
                "list": [{"id": "main", "model": {"primary": "m"}}],
                "dispatch": {"rules": [{"name": "dm", "agent": "main", "when": {"channel": "whatsapp", "sender": "owner"}}]},
            },
            "session": {"identity_links": {"owner": ["1"]}},
            "channels": {"whatsapp": {"group_allow_from": []}},
        }
        self.cfg_path = os.path.join(self.home, ".picoclaw/config.json")
        json.dump(cfg, open(self.cfg_path, "w"))
        db = sqlite3.connect(os.path.join(self.home, ".picoclaw/workspace/whatsapp/store.db"))
        db.execute("create table whatsmeow_lid_map (lid text, pn text)")
        db.execute("insert into whatsmeow_lid_map values ('900000000000001', '6281200000001')")
        db.commit()

    def run_tool(self, *args, ok=True):
        env = dict(os.environ, HOME=self.home, GROUP_AGENTS_NO_RESTART="1")
        p = subprocess.run([TOOL, *args], env=env, capture_output=True, text=True)
        self.assertEqual(p.returncode == 0, ok, p.stdout + p.stderr)
        return p.stdout

    def cfg(self):
        return json.load(open(self.cfg_path))

    def test_profile_trust_flow(self):
        self.run_tool("new", "club", G1)
        c = self.cfg()
        self.assertEqual({a["id"] for a in c["agents"]["list"]}, {"main", "club", "club-admin"})
        self.assertIn(G1, c["channels"]["whatsapp"]["group_allow_from"])
        rules = c["agents"]["dispatch"]["rules"]
        # owner rule must win over the group catch-all, which must win over DM rules
        self.assertEqual([r["agent"] for r in rules], ["club-admin", "club", "main"])
        self.assertTrue(os.path.exists(os.path.join(self.home, ".picoclaw/ws-club-admin/AGENT.md")))

        self.run_tool("trust", "club", "add", "0812-0000-0001", "Budi")
        self.run_tool("trust", "club", "add", "+6281200000001", "Budi")  # idempotent
        c = self.cfg()
        self.assertEqual(c["session"]["identity_links"]["trusted-6281200000001"][2], "900000000000001")
        senders = [r["when"].get("sender") for r in c["agents"]["dispatch"]["rules"]]
        self.assertEqual(senders.count("trusted-6281200000001"), 1)
        self.assertIn("6281200000001 Budi", self.run_tool("trust", "club", "list"))

        # a group added later inherits owner + trusted rules
        self.run_tool("add-group", "club", G2)
        rules = self.cfg()["agents"]["dispatch"]["rules"]
        g2 = [r for r in rules if r["when"].get("chat") == "group:" + G2]
        self.assertEqual([r["agent"] for r in g2], ["club-admin", "club-admin", "club"])

        self.run_tool("trust", "club", "remove", "081200000001")
        c = self.cfg()
        self.assertNotIn("trusted-6281200000001", c["session"]["identity_links"])
        self.assertFalse(any(r["when"].get("sender", "").startswith("trusted-") for r in c["agents"]["dispatch"]["rules"]))

    def test_rejects_bad_input(self):
        self.run_tool("new", "Bad Id", G1, ok=False)
        self.run_tool("new", "club", "not-a-group", ok=False)
        self.run_tool("trust", "nope", "add", "0812", ok=False)


if __name__ == "__main__":
    unittest.main()
