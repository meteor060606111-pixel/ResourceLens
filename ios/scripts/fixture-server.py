#!/usr/bin/env python3
"""Only a test origin; the shipping app never depends on this server."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import base64, hashlib, os, time

PNG=base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aJz8AAAAASUVORK5CYII=')
class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path=='/player-page':
            data=b'<html><meta property="og:video" content="https://player.bilibili.com/player.html?bvid=BV1xx411c7mD"><meta property="og:image" content="/sample.png"></html>'
            self.send_response(200);self.send_header('Content-Type','text/html');self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data);return
        if self.path in ['/page','/live.m3u8']:
            data=b'<html><video src="/sample.png"></video></html>' if self.path=='/page' else b'#EXTM3U\n#EXTINF:10,\nsegment.ts\n'
            self.send_response(200);self.send_header('Content-Type','text/html' if self.path=='/page' else 'application/vnd.apple.mpegurl');self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data);return
        if self.path=='/slow.bin':
            length=8*1024*1024;start=0;end=length-1
            if self.headers.get('Range','').startswith('bytes='):
                a,_,b=self.headers['Range'][6:].partition('-');start=int(a or 0);end=min(int(b) if b else end,end)
            self.send_response(206 if 'Range' in self.headers else 200)
            self.send_header('Content-Type','application/octet-stream');self.send_header('Accept-Ranges','bytes');self.send_header('ETag','"resourcelens-test-v1"');self.send_header('Last-Modified','Fri, 16 Oct 2026 00:00:00 GMT')
            self.send_header('Content-Length',str(end-start+1))
            if 'Range' in self.headers:self.send_header('Content-Range',f'bytes {start}-{end}/{length}')
            self.end_headers()
            try:
                for offset in range(start,end+1,16384):
                    self.wfile.write(bytes([42])*min(16384,end-offset+1));self.wfile.flush();time.sleep(.02)
            except (BrokenPipeError,ConnectionResetError):pass
            return
        if self.path=='/sample.mp4' and os.environ.get('RESOURCELENS_TEST_VIDEO'):
            data=Path(os.environ['RESOURCELENS_TEST_VIDEO']).read_bytes()
            self.send_response(200);self.send_header('Content-Type','video/mp4');self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data);return
        data=PNG if self.path=='/sample.png' else b'<html>not a video</html>'
        self.send_response(200);self.send_header('Content-Type','image/png' if self.path=='/sample.png' else 'text/html');self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
    def log_message(self,*args):pass
print('Fixture PNG SHA256 '+hashlib.sha256(PNG).hexdigest(),flush=True)
ThreadingHTTPServer(('127.0.0.1',18771),Handler).serve_forever()
