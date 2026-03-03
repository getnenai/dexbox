"""Secure value handling for sensitive workflow parameters.

SecureValue is a reference type — it holds a string identifier that maps
to a secret stored on the orchestrator.  The actual secret is never sent
to the sandbox container; instead, the sandbox sends the identifier back
to the parent via RPC, and the parent resolves it server-side.

Secure[T] is a Pydantic generic that allows workflow authors to annotate
parameters as sensitive while preserving type information for validation.
"""

from __future__ import annotations

from typing import Any, Generic, TypeVar

from pydantic_core import core_schema

T = TypeVar("T")


class SecureValue:
    """A reference to a secret stored on the server.

    Usage in workflow scripts::

        from dexbox import SecureValue

        def run(input: Input, secure_params: SecureParams) -> Result:
            # secure_params.password is a SecureValue — pass it directly
            computer.type(secure_params.password)
    """

    def __init__(self, identifier: str) -> None:
        self._identifier = identifier

    @property
    def identifier(self) -> str:
        return self._identifier

    def __repr__(self) -> str:
        return f"SecureValue({self._identifier!r})"

    def __str__(self) -> str:
        return f"<SecureValue:{self._identifier}>"


class Secure(Generic[T]):
    """Pydantic-aware generic for marking a field as a secure parameter.

    The inner type ``T`` is used for server-side validation of the secret
    value.  At the SDK level the field is always populated with a
    ``SecureValue`` instance containing the identifier.

    Example::

        from dexbox import Secure
        from pydantic import BaseModel

        class SecureParams(BaseModel):
            api_key: Secure[str]
            pin: Secure[int]
    """

    @classmethod
    def __class_getitem__(cls, item: type) -> Any:
        return type(
            f"Secure[{item.__name__}]",
            (),
            {
                "__get_pydantic_core_schema__": classmethod(
                    lambda _cls, _source, _handler: core_schema.no_info_plain_validator_function(
                        lambda v: v,
                        serialization=core_schema.plain_serializer_function_ser_schema(lambda v: str(v)),
                    )
                ),
                "_inner_type": item,
            },
        )
