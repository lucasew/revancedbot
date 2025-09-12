import logging
import sys
import tempfile
from pathlib import Path
import time
from typing import Optional
from github import Github
from dataclasses import dataclass
import subprocess
from selenium import webdriver
from selenium.webdriver.common.by import By
from selenium.webdriver.chrome.options import Options
from selenium.webdriver.support.ui import WebDriverWait
from tqdm import tqdm

logger = logging.getLogger()

@dataclass
class PatchJob:
    package_id: str
    package_version: Optional[str] # latest if None


class ApkpureFetcher():
    def __init__(self, location: Path):
        location.mkdir(parents=True, exist_ok=True)
        self.location = location
        prefs = {
            "download.default_directory": str(location.resolve()),
            "download.prompt_for_download": False,
            "download.directory_upgrade": True,
            "safebrowsing.enabled": True # without this it blocks because it can't check, anything better? send a PR please!
        }
        options = Options()
        options.add_experimental_option("prefs", prefs)
        self.driver = webdriver.Chrome(options=options)

    def url_from_job(self, job: PatchJob):
        return f"https://d.apkpure.com/b/APK/{job.package_id}?version={job.package_version or 'latest'}"

    def fetch(self, job: PatchJob):
        self.driver.get(self.url_from_job(job))

    def wait_settle(self):
        while True:
            logger.info("Checking if downloads are finished")
            pending = list(self.location.glob("*.crdownload"))
            if len(pending) == 0:
                break
            time.sleep(1)
        logger.info("Downloads finished, waiting for 5min")
        time.sleep(5)
        self.driver.close()


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
    logging.basicConfig()
    p = Patcher()
    jobs = list(p.jobs())
    if sys.argv[1] == 'jobs':
        for item in jobs:
            print(item, item.apkpure_url)
    elif sys.argv[1] == 'fetch':
        fetcher = ApkpureFetcher(Path("/tmp/revancedbot/apk"))
        jobs = jobs[:3]
        ops = tqdm(jobs, desc="Baixando apks")
        for job in ops:
            ops.set_description(f"Baixando {job.package_id}@{job.package_version or "latest"}")
            fetcher.fetch(job)
        fetcher.wait_settle()
    else:
        p(*sys.argv[1:])