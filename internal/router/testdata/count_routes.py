"""Count eligible legs, two-hop routes and voucher pairs in a router fixture.

An independent implementation of the router's leg eligibility rules
(Snapshot.eligible in internal/router/graph.go), so the Go
engine's counts on the production fixture are checked against something it
does not share code with. Writes production_graph.expected.json next to the
fixture:

    python3 -I internal/router/testdata/count_routes.py \
        internal/router/testdata/production_graph.json.gz
"""
import gzip
import json
import sys

PPM = 1_000_000


def fee_domain_ok(fee, proto):
    return fee < 10_000 or fee * (PPM + proto) < PPM * PPM


def count(fixture, plain_erc20):
    rows = fixture["rows"]
    pools = {p["ID"]: p for p in rows["Pools"]}
    tokens = {t["ID"]: t for t in rows["Tokens"]}
    pair_fees = {(f["Pool"].lower(), f["In"].lower(), f["Out"].lower()): f["FeePPM"] for f in (fixture["pairFees"] or [])}

    def routable_pool(p):
        if not p["Active"] or p["ChainState"] is None or not p["ChainState"]["QuoterVerified"]:
            return False
        quoter = (p["QuoterAddress"] or "").lower()
        no_quoter = quoter in ("", "0x" + "0" * 40)
        return no_quoter or p["QuoterType"].lower() == "relative"

    def routable_token(t):
        plain = t["VoucherType"].upper() == "GIFTABLE" or t["Address"].lower() in plain_erc20
        return t["Active"] and t["Decimals"] is not None and plain

    by_pool = {}
    for e in rows["Edges"]:
        if e["PoolID"] in pools and e["VoucherID"] in tokens:
            by_pool.setdefault(e["PoolID"], []).append(e)

    legs = []
    for pid, edges in by_pool.items():
        p = pools[pid]
        if not routable_pool(p):
            continue
        relative = (p["QuoterAddress"] or "").lower() not in ("", "0x" + "0" * 40)
        limited = (p["LimiterAddress"] or "").lower() not in ("", "0x" + "0" * 40)
        default, proto = p["ChainState"]["DefaultFeePPM"], p["ChainState"]["ProtocolFeePPM"]
        for a in edges:
            for b in edges:
                if a["VoucherID"] == b["VoucherID"]:
                    continue
                ta, tb = tokens[a["VoucherID"]], tokens[b["VoucherID"]]
                if not (routable_token(ta) and routable_token(tb)):
                    continue
                if not (a["Allowed"] and b["Allowed"]) or a["Balance"] is None or b["Balance"] is None:
                    continue
                if relative and (a["PriceIndex"] is None or b["PriceIndex"] is None):
                    continue
                if limited and int(a["Limit"] or 0) <= int(a["Balance"]):
                    continue
                available = int(b["Balance"]) - (int(b["FeesHeld"] or 0) if p["FeesDecoupled"] else 0)
                if available <= 0:
                    continue
                fee = pair_fees.get((p["Address"].lower(), ta["Address"].lower(), tb["Address"].lower()), default)
                if not fee_domain_ok(fee, proto):
                    continue
                legs.append((pid, a["VoucherID"], b["VoucherID"]))

    out_of = {}
    for leg in legs:
        out_of.setdefault(leg[1], []).append(leg)
    one_pairs = {(a, b) for _, a, b in legs}
    two_routes = 0
    pairs = set(one_pairs)
    for p1, a, b in legs:
        for p2, _, c in out_of.get(b, []):
            if p2 != p1 and c != a:
                two_routes += 1
                pairs.add((a, c))
    return {
        "legs": len(legs),
        "oneHopPairs": len(one_pairs),
        "twoHopRoutes": two_routes,
        "pairs": len(pairs),
    }


def main(path):
    with gzip.open(path) as f:
        fixture = json.load(f)
    erc20 = {t["Address"].lower() for t in fixture["rows"]["Tokens"] if t["VoucherType"].upper() == "ERC20"}
    expected = {
        "block": fixture["meta"]["block"],
        "plainGiftableOnly": count(fixture, set()),
        "withExternalERC20": count(fixture, erc20),
        "externalERC20": sorted(erc20),
    }
    out = path.replace(".json.gz", ".expected.json")
    with open(out, "w") as f:
        json.dump(expected, f, indent=2)
        f.write("\n")
    print(json.dumps(expected, indent=2))


if __name__ == "__main__":
    main(sys.argv[1])
