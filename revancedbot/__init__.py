import sys
import tempfile
from pathlib import Path
from typing import Optional
from github import Github
from dataclasses import dataclass
import subprocess


@dataclass
class PatchJob:
    package_id: str
    package_version: Optional[str] # latest if None

def parse_patch_jobs(data):
    for package in data.split("Package name: "):
        package_parts = package.split("Most common compatible versions:")
        if len(package_parts) != 2:
            continue
        package_id = package_parts[0].strip()
        rest = package_parts[1]
        for version in rest.split('\n'):
            version = version.strip().split(' ')[0]
            if version == '':
                continue
            yield PatchJob(package_id=package_id, package_version=None if version == 'Any' else version)



class Patcher():
    def __init__(self, tool_location: Path =None):
        if tool_location is None:
            tool_location = Path(tempfile.mkdtemp()).parent / "revancedbot"
        self.tool_location = tool_location
        self._started = None
    
    @property
    def patch_file(self):
        return self.tool_location / "patches.rvp"
    
    @property
    def patcher_file(self):
        return self.tool_location / "patcher.jar"

    def _startup(self):
        if self._started is not None:
            return
        g = Github()
        self.tool_location.mkdir(parents=True, exist_ok=True)
        if not self.patch_file.exists():
            latest_patch_release = g.get_repo("ReVanced/revanced-patches").get_latest_release()
            patch_asset = [p for p in latest_patch_release.assets if p.name.endswith(".rvp")][0]
            patch_asset.download_asset(self.patch_file)

        if not self.patcher_file.exists():
            latest_patcher_release = g.get_repo("ReVanced/revanced-cli").get_latest_release()
            patcher_asset = [p for p in latest_patcher_release.assets if p.name.endswith(".jar")][0]
            patcher_asset.download_asset(self.patcher_file)
        self._started = True

    def __call__(self, *args, stdin=None, stdout=None, stderr=None):
        self._startup()
        return subprocess.run(
            ["java", "-jar", self.patcher_file, args[0], self.patch_file, *args[1:]],
            stdin=stdin,
            stdout=stdout,
            stderr=stderr
        )
    
    def jobs(self):
        data = self("list-versions", stdout=subprocess.PIPE).stdout.decode()
        return parse_patch_jobs(data)



def run_patcher():
    p = Patcher()
    if sys.argv[1] == 'jobs':
        for item in p.jobs():
            print(item)
    else:
        p(*sys.argv[1:])