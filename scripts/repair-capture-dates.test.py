import importlib.util
from datetime import datetime
from pathlib import Path
from types import SimpleNamespace
import unittest

spec = importlib.util.spec_from_file_location('repair', Path(__file__).with_name('repair-capture-dates.py'))
repair = importlib.util.module_from_spec(spec)
spec.loader.exec_module(repair)


class RepairTests(unittest.TestCase):
    def test_folder_date_precedes_copy_dates(self):
        stat = SimpleNamespace(st_mtime=datetime(2020, 7, 5).timestamp(),
                               st_birthtime=datetime(2024, 5, 28).timestamp())
        result = repair.proposal('ALBUM/435 - Family (19.06.2020)/DSC_5573.jpg', stat)
        self.assertEqual(result['date'], '2020:06:19 00:00:00')
        self.assertEqual(result['source'], 'folder')

    def test_earliest_filesystem_date(self):
        stat = SimpleNamespace(st_mtime=datetime(2019, 7, 5).timestamp(),
                               st_birthtime=datetime(2024, 5, 28).timestamp())
        self.assertEqual(repair.proposal('Trip (19.06.2020)/a.jpg', stat)['source'], 'modified')

    def test_ranges(self):
        dates = list(repair.folder_dates('Trip (18-25.8.2024)/a.jpg'))
        self.assertEqual(min(dates), datetime(2024, 8, 18))

    def test_invalid_partial_and_filename_dates_ignored(self):
        self.assertEqual(list(repair.folder_dates('Trip 2017-2020 (31.02.2020)/2020-01-01.jpg')), [])

    def test_iso_folder(self):
        self.assertEqual(list(repair.folder_dates('Trip 2020-06-19/a.jpg')), [datetime(2020, 6, 19)])

    def test_existing_exif_is_preserved(self):
        path = str(Path('sample.jpg').resolve())
        row = {'path': path, 'date': '2020:01:01 00:00:00', 'candidates': []}
        repair.include_exif([row], [{'SourceFile': path, 'DateTimeOriginal': '2021:01:01 12:00:00'}])
        self.assertEqual(row['skip'], 'existing EXIF original')
        self.assertEqual(row['existing_date'], '2021:01:01 12:00:00')

    def test_exif_creation_candidate(self):
        path = str(Path('sample.jpg').resolve())
        row = {'path': path, 'date': '2020:01:01 00:00:00', 'candidates': []}
        repair.include_exif([row], [{'SourceFile': path, 'CreateDate': '2019:01:01 12:00:00'}])
        self.assertEqual(row['date'], '2019:01:01 12:00:00')
        self.assertEqual(row['source'], 'EXIF creation')


if __name__ == '__main__':
    unittest.main()
