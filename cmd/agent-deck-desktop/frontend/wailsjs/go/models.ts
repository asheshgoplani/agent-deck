export namespace main {
	
	export class SessionInfo {
	    id: string;
	    title: string;
	    projectPath: string;
	    groupPath: string;
	    tool: string;
	    status: string;
	    tmuxSession: string;
	    isRemote: boolean;
	    remoteHost?: string;
	
	    static createFrom(source: any = {}) {
	        return new SessionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.projectPath = source["projectPath"];
	        this.groupPath = source["groupPath"];
	        this.tool = source["tool"];
	        this.status = source["status"];
	        this.tmuxSession = source["tmuxSession"];
	        this.isRemote = source["isRemote"];
	        this.remoteHost = source["remoteHost"];
	    }
	}

}

