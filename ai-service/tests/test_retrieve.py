from app.rag.retrieve import Retriever


def test_crashloop_retrieves_pod_debug(built_index: Retriever):
    hits = built_index.search(
        ["CrashLoopBackOff", "container exit code", "application logs"],
        "CrashLoopBackOff",
        top_k=5,
        server_version="v1.31.0",
    )
    assert hits
    urls = " ".join(h.url + h.title for h in hits)
    assert "pod" in urls.lower() or "debug" in urls.lower() or "CrashLoop" in urls or hits[0].score > 0.2


def test_image_pull_retrieves_images(built_index: Retriever):
    hits = built_index.search(
        ["ImagePullBackOff", "ErrImagePull"],
        "ImagePullBackOff",
        top_k=4,
        server_version="v1.31.0",
    )
    assert any("images" in h.url.lower() or "Image" in h.title for h in hits)
