from urllib.request import urlopen
import sys
try:
    with urlopen('http://127.0.0.1:18080/health',timeout=3) as response:
        sys.exit(0 if response.status==200 else 1)
except Exception:
    sys.exit(1)
