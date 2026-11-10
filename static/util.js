// rpc calls a JSON-RPC 2.0 method at url.
// It resolves with the result, or rejects with an Error on HTTP or RPC failure.
export async function rpc(url, method, params) {
	let resp = await fetch(url, {
		method: "POST",
		headers: {"Content-Type": "application/json"},
		body: JSON.stringify({id: "0", jsonrpc: "2.0", method: method, params: params ?? {}}),
	});
	if (!resp.ok) {
		let text = await resp.text();
		throw new Error(`${resp.status} ${resp.statusText} ${text}`.trim());
	}
	let body = await resp.json();
	if (body.error) {
		let err = new Error(body.error.message || `rpc error ${body.error.code}`);
		err.code = body.error.code;
		throw err;
	}
	return body.result;
}
