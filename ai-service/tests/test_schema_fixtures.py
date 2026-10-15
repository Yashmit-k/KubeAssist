from app.schema_util import validate_snapshot


def test_fixtures_match_snapshot_schema(snapshots):
    for name, doc in snapshots.items():
        errs = validate_snapshot(doc)
        assert not errs, f"{name}: {errs}"
