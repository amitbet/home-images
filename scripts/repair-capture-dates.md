Missing capture date repair
===========================

`repair-capture-dates.py` creates a JSON plan from a HomeImages catalog opened
read-only. It leaves existing capture dates alone. For missing dates it chooses
the earliest plausible full folder date, file modification time or birth time.
Folder dates use day-month-year or year-month-day; day ranges with a shared
month/year use their start date. Inferred dates are estimates, not camera dates;
folder dates without a time use midnight. The JSON records all candidates and
the selected source. Years before 1900, future dates and invalid dates are ignored.

Generate a plan:

```powershell
python scripts/repair-capture-dates.py --catalog C:/ProgramData/HomeImages/index-go.sqlite3 --root 'D:/media - Images' --plan plan.json
```

Before applying, audit the files with ExifTool. Its JSON should include
`SourceFile`, `EXIF:DateTimeOriginal` and `EXIF:CreateDate`. Supply this as
`--existing-exif audit.json` when generating the plan to preserve capture dates
missed by the catalog and include EXIF creation time among the candidates.

Apply a reviewed plan using ExifTool:

```powershell
python scripts/repair-capture-dates.py --apply --plan plan.json --exiftool C:/tools/exiftool.exe
```

The source distribution also works with `--perl C:/path/to/perl.exe` and
`--exiftool C:/tools/exiftool/exiftool`. The writer checks EXIF again on disk,
rejects files changed since planning, preserves filesystem timestamps with `-P`,
retains ExifTool's `_original` backups, and reads back every written capture date.
Results go to `plan.json.results.jsonl`. Repeating the command resumes after
verified entries. Existing backups are never overwritten. Videos and formats
without a supported EXIF writer are skipped.

The filesystem watcher normally refreshes the catalog after each change.
Metadata edits change file signatures: existing AI/face results must be carried
forward only for these verified metadata-only edits, or recomputed. Keep the
plan, result log and backups until the catalog and face-filter results have been
checked. Do not delete backups automatically.
