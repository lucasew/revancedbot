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

logger = logging.getLogger(__name__)

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
            ["java", "-jar", self.patcher_file, *args],
            stdin=stdin,
            stdout=stdout,
            stderr=stderr
        )
    
    @property
    def jobs(self):
        data = self("list-versions", self.patch_file, stdout=subprocess.PIPE).stdout.decode()
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

class App:
    def __init__(self, root=Path("/tmp/revancedbot"), lowlimit=False):
        self.root = root
        self.patcher = Patcher(root/"patcher")
        self.lowlimit = lowlimit
        self._jobs = None
        self._fetched_apks = None

    @property
    def jobs(self):
        if self._jobs is None:
            self._jobs = list(self.patcher.jobs)
        if self.lowlimit:
            self._jobs = self._jobs[:3]
        return self._jobs

    @property
    def fetched_apks(self):
        apk_dir = self.root / "downloaded_apks"
        apk_dir.mkdir(parents=True, exist_ok=True)
        if self._fetched_apks is None:
            fetcher = ApkpureFetcher(apk_dir)
            logger.info("Baixando apks...")
            for job in self.jobs:
                logger.info(f"Baixando {job.package_id}@{job.package_version or "latest"}")
                fetcher.fetch(job)
            fetcher.wait_settle()
            self._fetched_apks = list(apk_dir.iterdir())
        return self._fetched_apks
    
    @property
    def patched_apks(self):
        apk_dir = self.root / "patched_apks"
        apk_dir.mkdir(parents=True, exist_ok=True)
        for fetched_apk in self.fetched_apks:
            try:
                logger.info(f"Patching {fetched_apk.name}...")
                self.patcher("patch", fetched_apk, "-o", apk_dir / fetched_apk.name, f"-p={self.patcher.patch_file}")
            except:
                pass

    

def run_patcher():
    logging.basicConfig(level=logging.DEBUG)
    a = App()
    if sys.argv[1] == 'jobs':
        for item in a.jobs:
            print(item, item.apkpure_url)
    elif sys.argv[1] == 'fetch':
        print(a.fetched_apks)
    elif sys.argv[1] == 'patch-all':
        print(a.patched_apks)
    else:
        a.patcher(*sys.argv[1:])