"""Fixed upstream, ingress-only transport. HA bearer remains mandatory upstream."""
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import re
from urllib.parse import unquote_to_bytes, urlsplit

UPSTREAM_HOST = '127.0.0.1'
UPSTREAM_PORT = 18085
INGRESS_ADDRESS = '172.30.32.2'
MAX_BODY = 65536
MAX_RESPONSE = 2 * 1024 * 1024
SPEAKER_ID = r'[a-z0-9][a-z0-9_-]{0,63}'
TARGET = r'(?:automation|script)\.[a-z0-9_]+'


def allowed_path(raw, method):
    """Reject query, encoded separators, ambiguous decoding and foreign URLs."""
    try:
        parsed = urlsplit(raw)
        if parsed.scheme or parsed.netloc or parsed.query or parsed.fragment or not raw.startswith('/'):
            return None
        decoded = unquote_to_bytes(parsed.path).decode('ascii')
    except (ValueError, UnicodeError):
        return None
    if '%' in decoded or '\\' in decoded or '//' in decoded or any(ord(c) < 32 for c in decoded):
        return None
    # Percent encodings are unnecessary in these IDs and can introduce a
    # second interpretation in HA ingress or an upstream HTTP implementation.
    if decoded != parsed.path:
        return None
    routes = {
        'GET': [r'/api/spotify', r'/api/music', r'/api/inventory', r'/api/access', r'/api/setup', r'/api/speakers', r'/api/assignments', r'/api/activity', r'/api/jobs/' + SPEAKER_ID, r'/api/speakers/' + SPEAKER_ID + r'/dictionary'],
        'POST': [r'/api/auto-update', r'/api/ai-check', r'/api/spotify/(?:login|callback|sync)', r'/api/speakers', r'/api/phrase-check', r'/api/dictionary/alias', r'/api/speakers/' + SPEAKER_ID + r'/(?:file-read|file-write|console|indicator|volume|airplay|ytm|player|ai|fingerprint|trust|check|install|adopt|reboot|confirm|rollback|restart|configure)'],
        'PATCH': [r'/api/speakers/' + SPEAKER_ID],
        'DELETE': [r'/api/speakers/' + SPEAKER_ID],
        'PUT': [r'/api/music', r'/api/access', r'/api/assignments/' + TARGET],
    }
    return decoded if any(re.fullmatch(pattern, decoded) for pattern in routes.get(method, [])) else None


class Handler(BaseHTTPRequestHandler):
    server_version = 'AliceIngress/1'

    def setup(self):
        super().setup()
        self.connection.settimeout(20)

    def log_message(self, *_args):
        pass

    def reply(self, status, payload, content_type='application/json; charset=utf-8'):
        body = payload if isinstance(payload, bytes) else json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(body)))
        self.send_header('Cache-Control', 'no-store')
        self.send_header('X-Content-Type-Options', 'nosniff')
        self.end_headers()
        self.wfile.write(body)

    def request(self):
        if self.client_address[0] != INGRESS_ADDRESS:
            return self.reply(403, {'error': 'Откройте панель через Home Assistant.'})
        path = allowed_path(self.path, self.command)
        if path is None:
            return self.reply(404, {'error': 'Неизвестный маршрут менеджера.'})
        authorization = self.headers.get('Authorization', '')
        if not authorization.startswith('Bearer ') or not authorization[7:] or len(authorization) > 10000:
            return self.reply(401, {'error': 'Войдите в Home Assistant заново.'})
        if self.headers.get('Transfer-Encoding') or len(self.headers.get_all('Content-Length', [])) > 1:
            return self.reply(400, {'error': 'Некорректный формат запроса.'})
        try:
            length = int(self.headers.get('Content-Length', '0'))
            if length < 0 or length > MAX_BODY:
                return self.reply(413, {'error': 'Запрос слишком большой.'})
            body = self.rfile.read(length) if length else None
            if body is not None and len(body) != length:
                return self.reply(400, {'error': 'Запрос передан не полностью.'})
        except (ValueError, TimeoutError, OSError):
            return self.reply(400, {'error': 'Не удалось прочитать запрос.'})
        # Do not forward ingress cookies, Origin, X-Supervisor-Token or user
        # identity headers. Upstream authenticates the real HA bearer itself.
        headers = {'Authorization': authorization, 'Accept': 'application/json'}
        if body is not None:
            headers['Content-Type'] = 'application/json'
        connection = http.client.HTTPConnection(UPSTREAM_HOST, UPSTREAM_PORT, timeout=15)
        try:
            connection.request(self.command, path, body=body, headers=headers)
            response = connection.getresponse()
            if 300 <= response.status < 400:
                return self.reply(502, {'error': 'Менеджер вернул неожиданный адрес. Перенаправление заблокировано.'})
            raw = response.read(MAX_RESPONSE + 1)
            if len(raw) > MAX_RESPONSE:
                return self.reply(502, {'error': 'Ответ менеджера слишком большой.'})
            # Only valid JSON is returned to HA, never raw upstream error pages.
            try:
                json.loads(raw)
            except (ValueError, UnicodeDecodeError):
                return self.reply(502, {'error': 'Менеджер вернул некорректный ответ.'})
            return self.reply(response.status, raw)
        except (TimeoutError, OSError, http.client.HTTPException):
            return self.reply(502, {'error': 'Нет связи с менеджером колонок. Проверьте сервер Алисы.'})
        finally:
            connection.close()

    do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = request


if __name__ == '__main__':
    ThreadingHTTPServer(('0.0.0.0', 8099), Handler).serve_forever()
