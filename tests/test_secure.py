"""Tests for Secure[T] generic type and Pydantic integration."""

from __future__ import annotations

from pydantic import BaseModel

from dexbox.secure_value import Secure, SecureValue

# ── Fixtures ──────────────────────────────────────────────────────────


class _ExampleParams(BaseModel):
    docs_password: Secure[str]  # type: ignore[type-arg,valid-type]
    docs_code: Secure[int]  # type: ignore[type-arg,valid-type]


# ── Tests ─────────────────────────────────────────────────────────────


class TestSecureGeneric:
    """Secure[T] generic type smoke tests."""

    def test_class_getitem_returns_a_type(self) -> None:
        """Secure[str] returns a type that can be used as a Pydantic field."""
        t = Secure[str]  # type: ignore[type-arg]
        assert isinstance(t, type)

    def test_different_inner_types_produce_different_names(self) -> None:
        """Secure[str] and Secure[int] are distinct types."""
        t_str = Secure[str]  # type: ignore[type-arg]
        t_int = Secure[int]  # type: ignore[type-arg]
        assert t_str.__name__ != t_int.__name__

    def test_inner_type_stored(self) -> None:
        """The inner type is stored as _inner_type."""
        t = Secure[str]  # type: ignore[type-arg]
        assert t._inner_type is str


class TestSecurePydanticValidation:
    """Pydantic model with Secure[T] fields accepts plain values."""

    def test_valid_str_and_int(self) -> None:
        m = _ExampleParams(docs_password="hunter2", docs_code=42)
        assert m.docs_password == "hunter2"
        assert m.docs_code == 42

    def test_arbitrary_values_pass_through(self) -> None:
        """The Secure validator is a no-op pass-through — all values are accepted."""
        m = _ExampleParams(docs_password=SecureValue("pw_id"), docs_code=SecureValue("code_id"))
        assert isinstance(m.docs_password, SecureValue)
        assert isinstance(m.docs_code, SecureValue)


class TestSecureSandboxModelConstruct:
    """model_construct populates fields with SecureValue refs."""

    def test_model_construct_with_secure_values(self) -> None:
        refs = _ExampleParams.model_construct(**{name: SecureValue(name) for name in _ExampleParams.model_fields})
        assert isinstance(refs.docs_password, SecureValue)
        assert isinstance(refs.docs_code, SecureValue)
        assert refs.docs_password.identifier == "docs_password"
        assert refs.docs_code.identifier == "docs_code"


class TestSecureImportFromDexbox:
    """Secure is importable from the top-level dexbox package."""

    def test_import(self) -> None:
        from dexbox import Secure as S

        assert S is Secure
