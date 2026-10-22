# Ordered simulation

Use `eth_simulateV1` to execute one block of up to 64 calls on private state.
The endpoint returns each call's status, gas use, return data, and committed logs.
It limits the request to three seconds and 50 million gas, or the lower configured RPC gas cap.
It never writes chain state or sends transactions.

Pass `transactionHash` in a call to use the original signed transaction from the local pool.
This extension preserves Kaia account keys, fee delegation, signatures, and nonce checks.
Other call fields do not change that signed transaction. A missing pool transaction fails the request.
Use ordinary Ethereum call fields for an unsigned backrun.

The endpoint applies next-block rules, governance base fee, and parent-hash storage initialization.
It estimates timestamp as parent timestamp plus one second and retains the parent proposer and randomness.
Do not use it to predict contracts that depend on an unknown next proposer, randomness, or exact timestamp.

State overrides are supported. Block overrides, transfer tracing, multiple blocks, and full transaction output fail explicitly.
`validation` enables nonce checks for unsigned calls. Signed pool targets always use native validation.
Unsigned calls use their real balance; the endpoint does not add gas funds.

Keep RPC ports bound to localhost. This endpoint adds simulation support; it does not change consensus or transaction gossip.
