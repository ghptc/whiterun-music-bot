"""Local-only HTTPS CONNECT fixture for disgo's constructor gateway lookup."""
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import ssl
import threading

from qa008_support import bounded_run


@contextmanager
def discord_constructor_fixture(work):
    """No forwarding code exists: accept only a canned gateway metadata GET."""
    work = Path(work)
    certificate, key = work / "fixture-ca.pem", work / "fixture-key.pem"
    command = ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
               "-keyout", str(key), "-out", str(certificate), "-days", "1",
               "-subj", "/CN=discord.com",
               "-addext", "subjectAltName=DNS:discord.com",
               "-addext", "basicConstraints=critical,CA:TRUE"]
    result = bounded_run(command, text=True, timeout=20)
    if result.returncode:
        raise RuntimeError("fixture TLS certificate generation failed: " + result.stderr)
    key.chmod(0o600)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain(certificate, key)
    requests = []

    class Gateway(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_GET(self):
            requests.append(dict(method="GET", path=self.path))
            if self.path != "/api/v10/gateway":
                self.send_error(502, "Unexpected fixture endpoint")
                return
            body = b'{"url":"wss://gateway.invalid/"}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    class Proxy(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_CONNECT(self):
            requests.append(dict(method="CONNECT", authority=self.path))
            if self.path != "discord.com:443":
                self.send_error(502, "External authorities are blocked")
                return
            self.send_response(200)
            self.end_headers()
            self.wfile.flush()
            encrypted = tls.wrap_socket(self.connection, server_side=True)
            try:
                Gateway(encrypted, self.client_address, self.server)
            finally:
                encrypted.close()

        def do_GET(self):
            requests.append(dict(method="BLOCKED", path=self.path))
            self.send_error(502, "No upstream network permitted")

        do_POST = do_GET

    server = ThreadingHTTPServer(("127.0.0.1", 0), Proxy)
    server.timeout = 5
    thread = threading.Thread(target=server.serve_forever, daemon=False)
    thread.start()
    proxy = f"http://127.0.0.1:{server.server_port}"
    try:
        yield {"HTTP_PROXY": proxy, "HTTPS_PROXY": proxy,
               "SSL_CERT_FILE": str(certificate)}, requests
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)
        if thread.is_alive():
            raise RuntimeError("fixture server thread survived shutdown")
