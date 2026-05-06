"""Regression tests for bridge.py Python version compatibility.

Background (#864): WSL Ubuntu 20.04 ships Python 3.8 by default. bridge.py
must import cleanly on 3.8+ so users on that platform can run the conductor
bridge without manually installing a newer Python.

The most common way this regresses is using runtime PEP 585 generics from
``collections.abc`` (e.g. ``Coroutine[X, Y, Z]``), which only became
subscriptable in 3.9. ``from __future__ import annotations`` does NOT cover
runtime-evaluated type aliases — only annotations.
"""

from __future__ import annotations

import ast
import sys
from pathlib import Path

import pytest

BRIDGE_PATH = Path(__file__).parent.parent / "bridge.py"

# collections.abc names that are NOT subscriptable until Python 3.9 (PEP 585).
COLLECTIONS_ABC_GENERICS = {
    "Coroutine", "Awaitable", "AsyncIterable", "AsyncIterator", "AsyncGenerator",
    "Iterable", "Iterator", "Generator", "Reversible", "Container",
    "Collection", "Callable", "Set", "MutableSet", "Mapping", "MutableMapping",
    "Sequence", "MutableSequence", "ByteString", "MappingView", "KeysView",
    "ItemsView", "ValuesView",
}


def _names_imported_from(tree: ast.AST, module: str) -> set[str]:
    names: set[str] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.ImportFrom) and node.module == module:
            for alias in node.names:
                names.add(alias.asname or alias.name)
    return names


def _runtime_subscripts(tree: ast.AST) -> list[tuple[str, int]]:
    """Return (name, lineno) for each ast.Subscript whose value is a Name,
    excluding subscripts that appear inside an annotation context (where
    ``from __future__ import annotations`` makes them lazy strings)."""
    annotation_nodes: set[int] = set()

    for node in ast.walk(tree):
        # Mark annotation subtrees so we can skip them.
        ann = None
        if isinstance(node, (ast.AnnAssign, ast.arg)):
            ann = node.annotation
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            ann = node.returns
        if ann is not None:
            for sub in ast.walk(ann):
                annotation_nodes.add(id(sub))

    hits: list[tuple[str, int]] = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Subscript) and id(node) not in annotation_nodes:
            value = node.value
            if isinstance(value, ast.Name):
                hits.append((value.id, node.lineno))
    return hits


def test_bridge_imports_on_current_python():
    """Smoke test: bridge.py must import on whatever Python runs the test."""
    src = BRIDGE_PATH.read_text()
    # Compile-only; full import requires optional deps (toml, aiogram).
    compile(src, str(BRIDGE_PATH), "exec")


def test_no_runtime_pep585_from_collections_abc():
    """Regression for #864.

    Subscripting ``collections.abc.Coroutine`` (etc.) at runtime crashes on
    Python 3.8 with ``TypeError: 'ABCMeta' object is not subscriptable``.
    The fix is to import those names from ``typing`` instead, which has
    been subscriptable since 3.5.
    """
    tree = ast.parse(BRIDGE_PATH.read_text())
    abc_imports = _names_imported_from(tree, "collections.abc") & COLLECTIONS_ABC_GENERICS
    if not abc_imports:
        return  # Nothing imported from collections.abc that could be subscripted.

    offenders = [
        (name, lineno)
        for name, lineno in _runtime_subscripts(tree)
        if name in abc_imports
    ]
    assert not offenders, (
        f"bridge.py uses runtime PEP 585 subscripts from collections.abc "
        f"that fail on Python 3.8: {offenders}. "
        f"Import these from `typing` instead, or move the alias into a "
        f"TYPE_CHECKING-only block."
    )


@pytest.mark.skipif(
    sys.version_info >= (3, 9),
    reason="Re-runs the import on whatever Python pytest uses; the 3.8 matrix "
    "in .github/workflows/python-compat.yml is the real gate.",
)
def test_bridge_imports_on_python_38():
    """When pytest itself is invoked under Python 3.8, the bridge must import.

    The CI matrix in ``.github/workflows/python-compat.yml`` runs this suite
    on 3.8, 3.9, 3.10, 3.11, and 3.12 to lock the floor.
    """
    import importlib.util

    spec = importlib.util.spec_from_file_location("bridge", BRIDGE_PATH)
    module = importlib.util.module_from_spec(spec)
    # Will raise TypeError on 3.8 if collections.abc.Coroutine[...] is used.
    spec.loader.exec_module(module)
