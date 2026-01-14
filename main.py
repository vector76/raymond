"""Entry point launcher - runs src.main as a module"""
import runpy

if __name__ == "__main__":
    # Run src.main as a module - this allows proper package imports without path hacks
    runpy.run_module("src.main", run_name="__main__")
