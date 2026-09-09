"""Check generated local HTML links and fragments without contacting external sites."""
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit

root = Path(__file__).resolve().parent / "build"


class Page(HTMLParser):
    def __init__(self, text):
        super().__init__()
        self.ids = set()
        self.links = []
        self.feed(text)

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if "id" in attrs:
            self.ids.add(attrs["id"])
        for attr in ("href", "src"):
            if attr in attrs:
                self.links.append(attrs[attr])


pages = {p: Page(p.read_text()) for p in root.rglob("*.html")}
assert pages, "Build the book before checking links"
errors = []
for path, page in pages.items():
    for link in page.links:
        url = urlsplit(link)
        if url.scheme or url.netloc:
            continue
        target = unquote(url.path)
        if target.startswith("/pulse/"):
            dest = root / target.removeprefix("/pulse/")
        elif target.startswith("/"):
            errors.append(f"{path.name}: link escapes /pulse/: {link}")
            continue
        else:
            dest = path.parent / target if target else path
        dest = dest.resolve()
        if dest.is_dir():
            dest /= "index.html"
        if not dest.is_file():
            errors.append(f"{path.name}: missing {link}")
        elif url.fragment and dest in pages and unquote(url.fragment) not in pages[dest].ids:
            errors.append(f"{path.name}: missing fragment {link}")
if errors:
    raise SystemExit("\n".join(errors))
print(f"Checked local assets, links and fragments in {len(pages)} HTML pages")
