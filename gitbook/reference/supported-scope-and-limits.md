# Supported scope and limits

- Classic `payment` operations in successful transactions. Path payments, `create_account`, offers, trustlines, claimable balances, Soroban operations and everything else are **counted by operation type per ledger and not compared one by one**. Count differences are disclosed and do not change the verdict.
- RPC events do not reproduce every Horizon effect, balance or history record, and this tool does not claim they do. The RPC adapter decodes transaction envelopes, not CAP-67 events.
- A finite comparison is evidence about that ledger range only.
- Tested live against testnet only. Mainnet endpoints use the same code but are not exercised here.
