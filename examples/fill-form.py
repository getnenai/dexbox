"""fill-form.py — fill out a web form using keyboard and mouse."""

from pydantic import AnyUrl, BaseModel

from dexbox import Agent, Computer


class Params(BaseModel):
    url: AnyUrl = "https://jsonformatter.curiousconcept.com/#"
    json_text: str = '{"key":"value"}'


class Result(BaseModel):
    pass


def run(params: Params) -> Result:
    agent = Agent()
    computer = Computer()

    # Navigate to the form
    agent.execute("Open web browser")
    computer.hotkey("ctrl", "l")
    computer.type(params.url)
    computer.press("Return")

    # Fill in test JSON value
    agent.execute("Click in the JSON data/URL text area", max_iterations=52)
    computer.type(params.json_text)
    agent.execute("Click process button", max_iterations=5)

    # Validate the test JSON
    if not agent.verify("Is the JSON valid?"):
        raise RuntimeError("Invalid JSON")

    return Result()
