from pydantic import BaseModel


class Params(BaseModel):
    name: str = "Test"


class Result(BaseModel):
    message: str


def run(params: Params) -> Result:
    with open("hello.txt", "w") as f:
        f.write(f"Hello {params.name} from assets!")

    import os

    os.makedirs("nested/dir", exist_ok=True)
    with open("nested/dir/data.json", "w") as f:
        f.write('{"status": "ok"}')

    return Result(message="Wrote files!")
