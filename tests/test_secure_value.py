"""Unit tests for dexbox.secure_value.SecureValue."""

from __future__ import annotations

from dexbox.secure_value import SecureValue


class TestSecureValue:
    """Tests for SecureValue construction and behavior."""

    def test_create_with_identifier(self) -> None:
        """SecureValue stores the given identifier."""
        sv = SecureValue("login_password")
        assert sv.identifier == "login_password"

    def test_repr_shows_identifier(self) -> None:
        """repr shows the identifier for debugging."""
        sv = SecureValue("my_secret")
        assert "my_secret" in repr(sv) and "SecureValue" in repr(sv)

    def test_str_shows_identifier(self) -> None:
        """str shows a masked representation."""
        sv = SecureValue("my_secret")
        assert "my_secret" in str(sv)

    def test_identifier_property(self) -> None:
        """identifier property returns the stored identifier."""
        sv = SecureValue("api_key")
        assert sv.identifier == "api_key"

    def test_import_from_dexbox(self) -> None:
        """SecureValue is importable from the top-level dexbox package."""
        from dexbox import SecureValue as SV

        assert SV is SecureValue

    def test_computer_type_isinstance_check(self) -> None:
        """SecureValue instances pass the isinstance check in Computer.type()."""
        sv = SecureValue("login_pw")
        assert isinstance(sv, SecureValue)
        # Simulate how Computer.type() dispatches on SecureValue
        if isinstance(sv, SecureValue):
            payload = {"secure_value_id": sv.identifier}
        else:
            payload = {"text": sv}
        assert payload == {"secure_value_id": "login_pw"}
