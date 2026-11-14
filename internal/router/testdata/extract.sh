#!/usr/bin/env bash
# Extracts the creation bytecode the anvil execution tests deploy, from forge
# build output of clc-protocol and calibur. Re-run after either repository
# changes and commit the result:
#
#   PROTOCOL=../clc-protocol CALIBUR=../calibur internal/router/testdata/extract.sh
#
# Last extracted from clc-protocol@ce090fd and calibur@249cac5 (v1.1.0).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
protocol="${PROTOCOL:-$here/../../../../clc-protocol}"
calibur="${CALIBUR:-$here/../../../../calibur}"
out="$here/contracts"
mkdir -p "$out"
extract() {
	python3 -I -c 'import json,sys; sys.stdout.write(json.load(open(sys.argv[1]))["bytecode"]["object"].removeprefix("0x"))' "$1" > "$out/$2.hex"
}
extract "$calibur/out/Calibur.sol/Calibur.json" Calibur
for c in ERC1967Factory GiftableToken SwapPool RelativeQuoter Limiter TokenUniqueSymbolIndex FeePolicy ProtocolFeeController; do
	extract "$protocol/out/$c.sol/$c.json" "$c"
done
extract "$protocol/out/SwapPool.t.sol/MockFeeOnTransferERC20.json" MockFeeOnTransferERC20
