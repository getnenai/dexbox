"""download-files.py — download files from the web and save to Drive."""

from pathlib import Path

from pydantic import AnyUrl, BaseModel, Field

from dexbox import Agent, Computer


class Params(BaseModel):
    url: AnyUrl = "https://raw.githubusercontent.com/python/cpython/main/README.rst"
    save_path: str = Field(default="/mnt/tmp")


class Result(BaseModel):
    pass


def run(params: Params) -> Result:
    url = params.url
    save_path = params.save_path

    agent = Agent()
    mount = Computer().drive(save_path)

    agent.execute(f"Open web browser to {url}. Save the file to {save_path}")
    for f in mount.files():
        Path(f.name).write_bytes(f.read_bytes())
        break

    return Result()
