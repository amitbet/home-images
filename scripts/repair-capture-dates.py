"""Plan missing capture dates from folder dates and filesystem timestamps.

Reads the HomeImages catalog without changing it. Plans are JSON audit records.
Apply uses ExifTool, preserves file timestamps, keeps _original backups, and
rechecks DateTimeOriginal on disk. Videos and formats without EXIF are skipped.
"""
import argparse
from collections import Counter
from datetime import datetime
import json
from pathlib import Path
import re
import sqlite3
import subprocess
import zlib


def folder_dates(path):
    # Full dates only: never guess a century, month, or missing year.
    for folder in Path(path).parts[:-1]:
        for pattern, order in [(r'(?<!\d)(\d{4})[-._](\d{1,2})[-._](\d{1,2})(?!\d)', 'ymd'),
                               (r'(?<!\d)(\d{1,2})[._-](\d{1,2})[._-](\d{4})(?!\d)', 'dmy')]:
            for match in re.finditer(pattern, folder):
                a, b, c = map(int, match.groups())
                try:
                    yield datetime(a, b, c) if order == 'ymd' else datetime(c, b, a)
                except ValueError:
                    pass
        # Shared month/year in a day range, e.g. 18-25.8.2024.
        for match in re.finditer(r'(?<!\d)(\d{1,2})\s*-\s*\d{1,2}[.](\d{1,2})[.](\d{4})(?!\d)', folder):
            d, m, y = map(int, match.groups())
            try:
                yield datetime(y, m, d)
            except ValueError:
                pass


def proposal(path, stat):
    candidates = [(d, 'folder') for d in folder_dates(path)]
    candidates.append((datetime.fromtimestamp(stat.st_mtime), 'modified'))
    if hasattr(stat, 'st_birthtime'):
        candidates.append((datetime.fromtimestamp(stat.st_birthtime), 'created'))
    candidates = [(d, source) for d, source in candidates if 1900 <= d.year and d <= datetime.now()]
    if not candidates:
        return None
    date, source = min(candidates, key=lambda pair: pair[0])
    return {'date': date.strftime('%Y:%m:%d %H:%M:%S'), 'source': source,
            'candidates': [{'date': d.isoformat(), 'source': s} for d, s in candidates]}


def plan(catalog, root):
    db = sqlite3.connect(Path(catalog).resolve().as_uri() + '?mode=ro', uri=True)
    rows = db.execute("SELECT path,metadata,id,signature FROM images WHERE nullif(taken_at,'') IS NULL ORDER BY path").fetchall()
    db.close()
    result = []
    root = Path(root).resolve()
    for relative, raw, image_id, signature in rows:
        path = (root / relative).resolve()
        row = {'path': str(path), 'relative': relative, 'id': image_id, 'signature': signature}
        result.append(row)
        if not path.is_relative_to(root):
            row['skip'] = 'outside root'
            continue
        if path.suffix.lower() not in {'.jpg', '.jpeg', '.tif', '.tiff', '.heic', '.heif', '.png', '.webp'}:
            row['skip'] = 'format without supported EXIF writer (including videos)'
            continue
        try:
            stat = path.stat()
            metadata = json.loads(zlib.decompress(raw[4:]) if isinstance(raw, bytes) and raw[:4] == b'HIM\x01' else raw)
            if metadata.get('taken_at'):
                row['skip'] = 'existing capture date'
                continue
            proposed = proposal(relative, stat)
            if not proposed:
                row['skip'] = 'no plausible date'
                continue
            row.update(proposed)
            row.update(size=stat.st_size, mtime_ns=stat.st_mtime_ns)
        except (OSError, ValueError) as error:
            row['skip'] = str(error)
    return result


def include_exif(rows, records):
    existing = {str(Path(r['SourceFile']).resolve()): r for r in records}
    for row in rows:
        if 'skip' in row:
            continue
        exif = existing.get(row['path'])
        if exif is None:
            row['skip'] = 'not present in EXIF audit'
            continue
        if exif.get('DateTimeOriginal'):
            row.update(skip='existing EXIF original', existing_date=exif['DateTimeOriginal'])
            continue
        try:
            date = datetime.strptime(exif.get('CreateDate', ''), '%Y:%m:%d %H:%M:%S')
            if 1900 <= date.year and date <= datetime.now():
                row['candidates'].append({'date': date.isoformat(), 'source': 'EXIF creation'})
                if exif['CreateDate'] < row['date']:
                    row.update(date=exif['CreateDate'], source='EXIF creation')
        except ValueError:
            pass
    return rows


class ExifTool:
    def __init__(self, command, error_log):
        self.errors = open(error_log, 'a', encoding='utf-8')
        self.process = subprocess.Popen(command + ['-stay_open', 'True', '-@', '-'],
                                        stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=self.errors, text=True, encoding='utf-8')

    def run(self, args):
        if any('\n' in arg or '\r' in arg for arg in args):
            raise ValueError('newline in ExifTool argument')
        self.process.stdin.write('\n'.join(['-charset', 'filename=UTF8'] + args + ['-execute']) + '\n')
        self.process.stdin.flush()
        output = []
        for line in self.process.stdout:
            if line.strip() == '{ready}':
                return ''.join(output)
            output.append(line)
        raise ValueError('ExifTool exited unexpectedly')

    def close(self):
        self.process.stdin.write('-stay_open\nFalse\n')
        self.process.stdin.flush()
        self.process.wait()
        self.errors.close()


def apply(rows, command, log):
    # Reuse one process, but flush each result so interrupted runs can resume.
    completed = set()
    if Path(log).exists():
        for line in Path(log).read_text(encoding='utf-8').splitlines():
            record = json.loads(line)
            if record.get('status') == 'verified':
                completed.add(record['path'])
    tool = ExifTool(command, log + '.errors.log')
    with open(log, 'a', encoding='utf-8') as audit:
        for row in rows:
            if 'skip' in row or row['path'] in completed:
                continue
            path = Path(row['path'])
            record = {'path': str(path), 'date': row['date']}
            try:
                stat = path.stat()
                if stat.st_size != row['size'] or stat.st_mtime_ns != row['mtime_ns']:
                    raise ValueError('file changed since plan')
                if Path(str(path) + '_original').exists():
                    raise ValueError('backup already exists; inspect before retrying')
                # EXIF is checked again independently of the catalog.
                before = tool.run(['-j', '-EXIF:DateTimeOriginal', str(path)])
                if json.loads(before)[0].get('DateTimeOriginal'):
                    raise ValueError('capture date already exists on disk')
                written = tool.run(['-P', '-if', 'not $EXIF:DateTimeOriginal',
                                    '-EXIF:DateTimeOriginal=' + row['date'], str(path)])
                after = tool.run(['-j', '-EXIF:DateTimeOriginal', str(path)])
                if json.loads(after)[0].get('DateTimeOriginal') != row['date']:
                    raise ValueError('verification failed: ' + written)
                record['status'] = 'verified'
            except (OSError, ValueError, subprocess.CalledProcessError) as error:
                record.update(status='skipped_or_failed', error=str(error))
            audit.write(json.dumps(record, ensure_ascii=False) + '\n')
            audit.flush()
            print(json.dumps(record), flush=True)
    tool.close()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--catalog')
    parser.add_argument('--root')
    parser.add_argument('--plan', required=True)
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--exiftool', default='exiftool')
    parser.add_argument('--perl', help='Perl executable when using the ExifTool source distribution')
    parser.add_argument('--existing-exif', help='ExifTool JSON audit including SourceFile, DateTimeOriginal, CreateDate')
    args = parser.parse_args()
    if args.apply:
        command = [args.perl, args.exiftool] if args.perl else [args.exiftool]
        apply(json.loads(Path(args.plan).read_text(encoding='utf-8')), command, args.plan + '.results.jsonl')
    else:
        if not args.catalog or not args.root:
            parser.error('--catalog and --root are required to generate a plan')
        rows = plan(args.catalog, args.root)
        if args.existing_exif:
            rows = include_exif(rows, json.loads(Path(args.existing_exif).read_text(encoding='utf-8-sig')))
        Path(args.plan).write_text(json.dumps(rows, indent=2, ensure_ascii=False), encoding='utf-8')
        print(json.dumps(dict(Counter(row.get('skip', row.get('source', 'unknown')) for row in rows)), indent=2))
