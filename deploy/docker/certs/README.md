# Extra trusted certificates for Docker builds

Only needed when the network re-signs HTTPS (company proxy, VPN, Zscaler, antivirus) and the
build fails with `x509: certificate signed by unknown authority` / `failed to verify certificate`.
Put that proxy's root certificate here as `<name>.crt` (PEM); the Go module and npm downloads in
deploy/docker/Dockerfile then trust it. `*.crt` files here are git-ignored.

Export it on macOS (Keychain Access trusts it already). Find its name from the failing host:

    openssl s_client -connect proxy.golang.org:443 </dev/null 2>/dev/null | grep -m1 'issuer'

then export the matching root certificate:

    security find-certificate -a -p -c "<issuer name>" /Library/Keychains/System.keychain > deploy/docker/certs/company.crt

(or Keychain Access → System → the certificate → File → Export Items… → format PEM, `.crt` name).
