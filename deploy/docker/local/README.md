# Machine-specific Docker build inputs

Everything here except this README is git-ignored. Both files are optional.

## `.npmrc`: npm registry and credentials

`scripts/docker.sh` copies it here from `NPMRC` (command line or `deploy/.env`):

    NPMRC=~/.npmrc scripts/docker.sh start

The web demo build then uses its registry and credentials. It stays in the build stage, not
in the final image, but Docker keeps that stage's layers in its local build cache.
`NPM_REGISTRY=<url>` overrides the registry in it.

## `*.crt`: certificate of a proxy that re-signs HTTPS

Only needed when the network re-signs HTTPS (company proxy, VPN, Zscaler, antivirus) and the
build fails with `x509: certificate signed by unknown authority` / `failed to verify certificate`.
Put that proxy's root certificate here as `<name>.crt` (PEM); the Go module and npm downloads
then trust it.

Find its name from the failing host:

    openssl s_client -connect proxy.golang.org:443 </dev/null 2>/dev/null | grep -m1 issuer

and export the matching root certificate (macOS already trusts it):

    security find-certificate -a -p -c "<issuer name>" /Library/Keychains/System.keychain > deploy/docker/local/company.crt

(or Keychain Access → the certificate → File → Export Items… → PEM, saved with a `.crt` name).
