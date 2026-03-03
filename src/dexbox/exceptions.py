"""Exceptions for dexbox SDK."""


class WorkflowError(Exception):
    """Base exception for workflow errors."""


class RPCError(WorkflowError):
    """Error communicating with the parent service."""


class TimeoutError(WorkflowError):
    """Operation timed out."""


class ValidationError(WorkflowError):
    """Input/output validation error."""
