# Security policy

eventparity-engine reads public chain data over HTTPS and writes local files. It holds no keys, signs nothing and submits nothing. (`tools/mkcorpus` submits testnet transactions with throwaway in-memory keys to build fixtures; it is a development tool, not part of the engine.)

Report vulnerabilities (for example a candidate stream that makes a comparison report parity when it should not, a path that makes the CLI overwrite or read an unintended file, or a way to leak a provider URL's credentials into a stream or report) through GitHub's private vulnerability reporting for this repository. Please do not open a public issue for them.

Provider URLs may embed API keys: streams and reports record the origin (scheme and host) only, and error messages omit query strings.
