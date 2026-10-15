from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from app.rag.index import build_index  # noqa: E402


def main() -> None:
    n = build_index()
    print(f"indexed {n} chunks")


if __name__ == "__main__":
    main()
