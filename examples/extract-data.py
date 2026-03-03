"""extract-data.py — extract structured data from a web page."""

from pydantic import BaseModel, Field

from dexbox import Agent


class Params(BaseModel):
    url: str = "https://news.ycombinator.com"


class Story(BaseModel):
    rank: int = Field(gt=0)
    title: str = Field(min_length=1)
    points: int = Field(default=0, ge=0)


class Result(BaseModel):
    stories: list[Story] = Field(default_factory=list[Story])


def run(params: Params) -> Result:
    agent = Agent()

    # Navigate to the page
    agent.execute(f"Open web browser and navigate to {params.url}")

    # Extract top stories as structured data
    data = agent.extract(
        "Extract the top 5 stories from this Hacker News page",
        schema=Result.model_json_schema(),
    )

    return Result.model_construct(**data)
