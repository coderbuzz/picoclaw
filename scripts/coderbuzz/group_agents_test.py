"""Run: python3 scripts/coderbuzz/group_agents_test.py"""
import json
import os
import sqlite3
import subprocess
import tempfile
import unittest

TOOL = os.path.join(os.path.dirname(os.path.abspath(__file__)), "group-agents")
G1, G2 = "120000000000000001@g.us", "120000000000000002@g.us"
PN, LID = "6281200000001", "900000000000001"


class GroupAgentsTest(unittest.TestCase):
    def setUp(self):
        self.home = tempfile.mkdtemp()
        self.pico = os.path.join(self.home, ".picoclaw")
        os.makedirs(os.path.join(self.pico, "workspace/whatsapp"))
        cfg = {
            "agents": {
                "list": [{"id": "main", "model": {"primary": "m"}}],
                "dispatch": {"rules": [{"name": "dm", "agent": "main", "when": {"channel": "whatsapp", "sender": "owner"}}]},
            },
            "session": {"identity_links": {"owner": ["1"]}},
            "channels": {"whatsapp": {"group_allow_from": []}},
        }
        self.cfg_path = os.path.join(self.pico, "config.json")
        with open(self.cfg_path, "w") as f:
            json.dump(cfg, f)
        db = sqlite3.connect(os.path.join(self.pico, "workspace/whatsapp/store.db"))
        db.execute("create table whatsmeow_lid_map (lid text, pn text)")
        db.execute("insert into whatsmeow_lid_map values (?, ?)", (LID, PN))
        db.commit()
        db.close()

    def run_tool(self, *args, ok=True, cwd=None):
        env = dict(os.environ, HOME=self.home, GROUP_AGENTS_NO_RESTART="1")
        p = subprocess.run([TOOL, *args], env=env, capture_output=True, text=True, cwd=cwd or self.home)
        self.assertEqual(p.returncode == 0, ok, p.stdout + p.stderr)
        return p.stdout + p.stderr

    def cfg(self):
        with open(self.cfg_path) as f:
            return json.load(f)

    def ws(self, name):
        return os.path.join(self.pico, name)

    def test_profile_trust_flow(self):
        self.run_tool("new", "club", G1)
        c = self.cfg()
        self.assertEqual({a["id"] for a in c["agents"]["list"]}, {"main", "club", "club-admin", "club-staff"})
        self.assertIn(G1, c["channels"]["whatsapp"]["group_allow_from"])
        # owner rule must win over the group catch-all, which must win over DM rules
        self.assertEqual([r["agent"] for r in c["agents"]["dispatch"]["rules"]], ["club-admin", "club", "main"])
        self.assertIn("profil club sudah lengkap", self.run_tool("new", "club", G1))
        by_id = {a["id"]: a for a in c["agents"]["list"]}
        shared = self.ws("ws-club")
        iso = {"enabled": True, "expose_paths": [{"source": shared, "target": shared, "mode": "rw"}]}
        self.assertEqual(by_id["club-admin"]["isolation"], iso)
        self.assertEqual(by_id["club-staff"]["isolation"], iso)
        self.assertNotIn("isolation", by_id["club"])

        # the owner's group agent grants trust from a quoted LID
        self.run_tool("trust", "club", "add", LID, "Budi", cwd=self.ws("ws-club-admin"))
        self.run_tool("trust", "club", "add", "0812-0000-0001", "Budi", cwd=self.ws("workspace"))  # same person, idempotent
        c = self.cfg()
        self.assertEqual(c["session"]["identity_links"]["trusted-" + PN], [PN, PN + "@s.whatsapp.net", LID, LID + "@lid"])
        staff = [r for r in c["agents"]["dispatch"]["rules"] if r["agent"] == "club-staff"]
        self.assertEqual(len(staff), 1)
        self.assertIn(PN + " Budi", self.run_tool("trust", "club", "list"))

        # a group added later inherits owner + trusted rules
        self.run_tool("add-group", "club", G2)
        g2 = [r["agent"] for r in self.cfg()["agents"]["dispatch"]["rules"] if r["when"].get("chat") == "group:" + G2]
        self.assertEqual(g2, ["club-admin", "club-staff", "club"])

        self.run_tool("trust", "club", "remove", "081200000001")
        c = self.cfg()
        self.assertNotIn("trusted-" + PN, c["session"]["identity_links"])
        self.assertFalse(any(r["agent"] == "club-staff" for r in c["agents"]["dispatch"]["rules"]))

    def test_new_repairs_isolation_without_overwriting(self):
        self.run_tool("new", "club", G1)
        c = self.cfg()
        by_id = {a["id"]: a for a in c["agents"]["list"]}
        by_id["club-admin"].pop("isolation")  # profile from before isolation existed
        by_id["club-staff"]["isolation"] = {"enabled": True, "network": True}  # hand-tuned
        with open(self.cfg_path, "w") as f:
            json.dump(c, f)
        self.assertIn("isolasi club-admin", self.run_tool("new", "club"))
        by_id = {a["id"]: a for a in self.cfg()["agents"]["list"]}
        self.assertTrue(by_id["club-admin"]["isolation"]["enabled"])
        self.assertEqual(by_id["club-staff"]["isolation"], {"enabled": True, "network": True})
        self.assertIn("sudah lengkap", self.run_tool("new", "club"))

    def test_only_owner_agents_change_trust(self):
        self.run_tool("new", "club", G1)
        self.run_tool("new", "other", G2)
        for ws in ("ws-club-staff", "ws-club", "ws-other-admin"):
            self.run_tool("trust", "club", "add", PN, cwd=self.ws(ws), ok=False)
        self.run_tool("new", "evil", cwd=self.ws("ws-club-admin"), ok=False)
        self.run_tool("trust", "club", "list", cwd=self.ws("ws-club-staff"))  # read-only is fine

    def test_groups_lists_profiles(self):
        self.run_tool("groups", ok=False)  # no list yet
        with open(os.path.join(self.pico, "workspace/whatsapp/groups.json"), "w") as f:
            json.dump([{"jid": G1, "name": "Klub", "participants": 9}, {"jid": G2, "name": "Lain", "participants": 3}], f)
        self.run_tool("new", "club", G1)
        out = self.run_tool("groups").splitlines()
        self.assertEqual(out, [f"{G1}\t9 anggota\tprofil: club\tKlub", f"{G2}\t3 anggota\tprofil: -\tLain"])

    def test_rejects_bad_input(self):
        self.run_tool("new", "Bad Id", G1, ok=False)
        self.run_tool("new", "club", "not-a-group", ok=False)
        self.run_tool("trust", "nope", "add", "0812", ok=False)


if __name__ == "__main__":
    unittest.main()
