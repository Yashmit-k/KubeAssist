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


import pytest


@pytest.mark.parametrize(
    "ftype,expect",
    [
        ("OOMKilled", "memory"),
        ("FailedScheduling", "schedul"),
        ("ServiceSelectorMismatch", "service"),
        ("CreateContainerConfigError", "configmap"),
        ("VolumeMountFailure", "volume"),
        ("InitContainerFailure", "init"),
        ("Evicted", "evict"),
        ("ProbeFailure", "probe"),
    ],
)
def test_top_hit_is_on_topic(built_index: Retriever, ftype, expect):
    hits = built_index.search([], ftype, top_k=3)
    assert ftype in hits[0].failure_tags
    assert any(expect in (h.url + h.title).lower() for h in hits)


def test_results_span_distinct_pages(built_index: Retriever):
    hits = built_index.search(["CrashLoopBackOff"], "CrashLoopBackOff", top_k=5)
    urls = [h.url for h in hits]
    assert max(urls.count(u) for u in urls) <= 2
