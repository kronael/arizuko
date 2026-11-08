#!/usr/bin/env python3
"""Check estate HTML before delivery."""
import argparse
from html.parser import HTMLParser
from pathlib import Path
import re
import sys
from urllib.parse import unquote, urlsplit

import gen_site


class Node:
    def __init__(self, tag, attrs, line, parent=None):
        self.tag = tag
        self.attrs = dict(attrs)
        self.line = line
        self.parent = parent
        self.children = []

    def text(self):
        return " ".join(child.text() if isinstance(child, Node) else child for child in self.children)

    def classes(self):
        return self.attrs.get("class", "").split()

    def descendants(self, tag=None):
        for child in self.children:
            if isinstance(child, Node):
                if tag is None or child.tag == tag:
                    yield child
                yield from child.descendants(tag)


class Parser(HTMLParser):
    VOID = {"area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr"}

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.root = Node("root", [], 1)
        self.current = self.root

    def handle_starttag(self, tag, attrs):
        node = Node(tag, attrs, self.getpos()[0], self.current)
        self.current.children.append(node)
        if tag not in self.VOID:
            self.current = node

    def handle_startendtag(self, tag, attrs):
        self.handle_starttag(tag, attrs)
        if tag not in self.VOID:
            self.handle_endtag(tag)

    def handle_endtag(self, tag):
        node = self.current
        while node.parent is not None:
            if node.tag == tag:
                self.current = node.parent
                return
            node = node.parent

    def handle_data(self, data):
        self.current.children.append(data)


def numeric(text):
    text = text.strip().lower().replace("\u00a0", " ").replace("\u202f", " ")
    text = re.sub(r"(?:kč|czk|eur|€|m²|m2|ha|%|ks|rok(?:y|ů)?|měsíc(?:e|ů)?|let|min)", "", text)
    return bool(re.search(r"\d", text)) and bool(re.fullmatch(r"[\d\s.,+−–—/\-]+", text))


def check_page(root, report):
    for table in root.descendants("table"):
        rows = list(table.descendants("tr"))
        body = []
        invalid = "stack" in table.classes()
        for row in rows:
            cells = [c for c in row.children if isinstance(c, Node) and c.tag in {"th", "td"}]
            if len(cells) > 3:
                invalid = True
            values = [c for c in cells if c.tag == "td"]
            if values:
                body.append(values)
                if len(values) < 2 or any("n" not in c.classes() or not numeric(c.text()) for c in values[1:]):
                    invalid = True
        if not body or invalid:
            report(table, "N1", "table must contain a label and numeric columns, at most three columns")
    for node in root.descendants():
        if node.tag in {"h1", "h2", "h3", "h4", "h5", "h6"}:
            title = " ".join(node.text().split()).casefold()
            if title.startswith(("co nedělat", "co nežádat")):
                report(node, "N2", "put restrictions in the relevant action")


def check_index(root, cfg, site, report):
    rows = {row["id"]: row for row in gen_site.parse_db(cfg["db"])}
    valuations = gen_site.find_valuations(str(site))
    href_to_id = {entry["href"]: idn for idn, entry in valuations.items()}
    seen = set()
    for node in root.descendants():
        if node.tag in {"h1", "h2", "h3", "h4", "h5", "h6"} and " ".join(node.text().split()).casefold() == cfg["labels"]["valuations"].casefold():
            report(node, "N3", "standalone valuation section")
        if node.tag == "a":
            href = unquote(urlsplit(node.attrs.get("href", "")).path).removeprefix("./")
            idn = href_to_id.get(href)
            if idn is not None:
                card = node.parent
                while card is not None and "item" not in card.classes():
                    card = card.parent
                match = re.search(r"\bID\s+(\d+)\b", card.text()) if card else None
                listing_links = [a.attrs.get("href") for a in card.descendants("a")] if card else []
                if not card or not match or match.group(1) != idn or idn not in rows or not rows[idn]["active"] or rows[idn]["url"] not in listing_links:
                    report(node, "N3", "valuation must be inside its active listing card")
                else:
                    seen.add(idn)
        if "meta" in node.classes():
            card = node.parent
            while card is not None and "item" not in card.classes():
                card = card.parent
            if not card:
                continue
            classes = set(node.classes())
            text = node.text()
            match = re.search(r"\bID\s+(\d+)\b", text)
            row = rows.get(match.group(1)) if match else None
            if row and gen_site.digits(row["minutes"]) is not None and not any(dot in text for _, dot in gen_site.ZONES.values()):
                report(node, "N4", "listing with minutes requires a dot")
            for zone, dot in gen_site.ZONES.values():
                if (zone in classes and any(other in text for other in ("🟢", "🟡", "🔴") if other != dot)) or (dot in text and zone not in classes):
                    report(node, "N4", "colour and dot disagree")
                    break
    for idn in valuations:
        if idn in rows and rows[idn]["active"] and idn not in seen:
            report(root, "N3", f"missing valuation link for listing {idn}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config")
    parser.add_argument("files", nargs="+")
    args = parser.parse_args()
    failures = []
    try:
        cfg = None
        if args.config:
            path = Path(args.config).expanduser()
            if not path.is_file():
                raise ValueError(f"configuration not found: {path}")
            cfg = gen_site.load_config(str(path))
        for name in args.files:
            path = Path(name).expanduser()
            if path.suffix.lower() != ".html" or not path.is_file():
                raise ValueError(f"not an existing HTML file: {path}")
            content = path.read_text(encoding="utf-8")
            if not content.strip():
                raise ValueError(f"empty HTML file: {path}")
            doc = Parser()
            doc.feed(content)
            def report(node, rule, message):
                failures.append(f"{path}:{node.line}: {rule}: {message}")
            check_page(doc.root, report)
            if path.name == "index.html":
                if cfg is None:
                    raise ValueError(f"{path}: index requires --config")
                if path.resolve() == (Path(cfg["site"]).expanduser() / "index.html").resolve():
                    check_index(doc.root, cfg, path.parent, report)
    except (OSError, ValueError, KeyError) as error:
        print(error, file=sys.stderr)
        return 1
    if failures:
        print("\n".join(failures))
        return 1
    print("OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
