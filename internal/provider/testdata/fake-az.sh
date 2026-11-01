#!/bin/sh
# Fake Azure CLI for the example azure-vm provider tests. No cloud calls.
#
# State dir $FAKE_AZ_STATE:
#   argv.log        one line per call: the argv, space-joined
#   env.log         one line per call: AZURE_CONFIG_DIR=<value>
#   login.stdin     what `az login ... --password @-` got on stdin
#   rules.log       one line per nsg rule: its source prefix
#   custom-data     a copy of the file vm create got as --custom-data
#   resources.json  the fake resource group: a JSON list of resources
#                   (tests may seed it)
# Env:
#   FAKE_AZ_FAIL    "<words>": fail the call whose leading words match, for
#                   example "vm create" or "vm run-command"
#   FAKE_AZ_READY   "no": run-command reports cloud-init did not finish
#   FAKE_VM_IP      the VMs' public IP (default 192.0.2.10)
#   FAKE_AZ_UNTAG_AFTER_LIST  a resource name whose tags are dropped right
#                   after `resource list` answers (someone retagged it)
s="${FAKE_AZ_STATE:?FAKE_AZ_STATE not set}"
printf '%s\n' "$*" >>"$s/argv.log"
printf 'AZURE_CONFIG_DIR=%s\n' "${AZURE_CONFIG_DIR:-}" >>"$s/env.log"
exec python3 -I -c '
import json, os, shutil, sys

s = os.environ["FAKE_AZ_STATE"]
argv = sys.argv[1:]
words, opts, cur = [], {}, None
for a in argv:
    if a.startswith("--"):
        cur = a
        opts.setdefault(cur, [])
    elif cur is None:
        words.append(a)
    else:
        opts[cur].append(a)

def opt(name, default=""):
    v = opts.get(name)
    return v[0] if v else default

def fail(msg, code=1):
    sys.stderr.write("ERROR: " + msg + "\n")
    sys.exit(code)

cmdline = " ".join(words)
f = os.environ.get("FAKE_AZ_FAIL", "")
if f and cmdline.startswith(f):
    fail("(FakeFailure) %s failed on purpose" % f)

path = os.path.join(s, "resources.json")
try:
    with open(path) as fh:
        res = json.load(fh)
except OSError:
    res = []

def save():
    with open(path, "w") as fh:
        json.dump(res, fh)

SUB = "/subscriptions/00000000-0000-0000-0000-000000000000"

def rid(rg, typ, name):
    return "%s/resourceGroups/%s/providers/%s/%s" % (SUB, rg, typ, name)

def tags():
    out = {}
    for t in opts.get("--tags", []):
        k, _, v = t.partition("=")
        out[k] = v
    return out

def add(typ, extra=None):
    rg, name = opt("--resource-group"), opt("--name")
    r = {"id": rid(rg, typ, name), "name": name, "type": typ, "resourceGroup": rg, "tags": tags()}
    r.update(extra or {})
    res.append(r)
    save()
    return r

def find_id(i):
    for r in res:
        if r["id"].lower() == i.lower():
            return r
    return None

def vm_view(r):
    v = dict(r)
    v["publicIps"] = os.environ.get("FAKE_VM_IP", "192.0.2.10")
    v["privateIps"] = "10.77.0.4"
    v["powerState"] = "VM running"
    return v

def not_found(what):
    fail("(ResourceNotFound) The Resource %s was not found." % what, 3)

w = words
if w[:1] == ["login"]:
    if "--service-principal" not in argv or opt("--password") != "@-":
        fail("fake az: login must use --service-principal --password @-")
    with open(os.path.join(s, "login.stdin"), "w") as fh:
        fh.write(sys.stdin.read())
elif w[:2] == ["group", "show"]:
    print(json.dumps({"name": opt("--name"), "location": "westeurope"}))
elif w[:3] == ["network", "nsg", "create"]:
    add("Microsoft.Network/networkSecurityGroups")
elif w[:4] == ["network", "nsg", "rule", "create"]:
    with open(os.path.join(s, "rules.log"), "a") as fh:
        fh.write(" ".join(opts.get("--source-address-prefixes", [])) + "\n")
elif w[:3] == ["network", "vnet", "create"]:
    add("Microsoft.Network/virtualNetworks")
elif w[:3] == ["network", "public-ip", "create"]:
    add("Microsoft.Network/publicIPAddresses")
elif w[:3] == ["network", "nic", "create"]:
    add("Microsoft.Network/networkInterfaces")
elif w[:2] == ["vm", "create"]:
    shutil.copyfile(opt("--custom-data"), os.path.join(s, "custom-data"))
    rg, name = opt("--resource-group"), opt("--name")
    disk = {"id": rid(rg, "Microsoft.Compute/disks", opt("--os-disk-name")), "name": opt("--os-disk-name"),
            "type": "Microsoft.Compute/disks", "resourceGroup": rg, "tags": {}}
    res.append(disk)
    nic = rid(rg, "Microsoft.Network/networkInterfaces", opt("--nics"))
    add("Microsoft.Compute/virtualMachines", {"storageProfile": {"osDisk": {"managedDisk": {"id": disk["id"]}}},
        "cascade": [disk["id"], nic]})
elif w[:2] == ["vm", "show"]:
    for r in res:
        if r["type"] == "Microsoft.Compute/virtualMachines" and r["name"] == opt("--name") and r["resourceGroup"] == opt("--resource-group"):
            print(json.dumps(vm_view(r)))
            break
    else:
        not_found(opt("--name"))
elif w[:2] == ["vm", "list"]:
    print(json.dumps([vm_view(r) for r in res if r["type"] == "Microsoft.Compute/virtualMachines" and r["resourceGroup"] == opt("--resource-group")]))
elif w[:3] == ["vm", "run-command", "invoke"]:
    ready = "" if os.environ.get("FAKE_AZ_READY") == "no" else "ready=yes\n"
    msg = "Enable succeeded: \n[stdout]\n" + ready + "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGn3PMci37qtGRr0qkRvnuvhHEkIl6OupFa0wrBJQenu root@vm\n\n[stderr]\n"
    print(json.dumps({"value": [{"code": "ProvisioningState/succeeded", "message": msg}]}))
elif w[:2] == ["resource", "tag"]:
    r = find_id(opt("--ids"))
    if r is None:
        not_found(opt("--ids"))
    r["tags"] = tags()
    save()
elif w[:2] == ["resource", "list"]:
    print(json.dumps([r for r in res if r["resourceGroup"] == opt("--resource-group")]))
    untag = os.environ.get("FAKE_AZ_UNTAG_AFTER_LIST", "")
    for r in res:
        if untag and r["name"] == untag:
            r["tags"] = {}
    save()
elif w[:2] == ["resource", "show"]:
    r = find_id(opt("--ids"))
    if r is None:
        not_found(opt("--ids"))
    print(json.dumps(r))
elif w[:2] == ["resource", "delete"]:
    r = find_id(opt("--ids"))
    if r is None:
        not_found(opt("--ids"))
    gone = set([r["id"].lower()] + [c.lower() for c in r.get("cascade", [])])
    res[:] = [x for x in res if x["id"].lower() not in gone]
    save()
else:
    fail("fake az: unexpected command: " + cmdline)
' "$@"
